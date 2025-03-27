package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/microcosm-cc/bluemonday"
	"github.com/vasilisp/wikai/internal/api"
	"github.com/vasilisp/wikai/internal/data"
	"github.com/vasilisp/wikai/internal/openai"
	"github.com/vasilisp/wikai/internal/sqlite"
	"github.com/vasilisp/wikai/internal/util"
	"github.com/yuin/goldmark"
)

type ctx struct {
	config *config
	db     *sql.DB
	openai *openai.Client
}

func newCtx() *ctx {
	config := loadConfig()
	util.Assert(config != nil, "newCtx nil config")

	db := initSqlite(config)
	util.Assert(db != nil, "newCtx nil DB")

	openai := openai.NewClient(config.OpenAIToken, config.EmbeddingDimensions)

	return &ctx{
		config: config,
		db:     db,
		openai: openai,
	}
}

func (ctx *ctx) Close() {
	util.Assert(ctx != nil, "Close nil ctx")
	util.Assert(ctx.db != nil, "Close nil DB")
	ctx.db.Close()
}

func index(ctx *ctx, page *api.Page) error {
	util.Assert(ctx != nil, "index nil ctx")
	util.Assert(page != nil, "index nil page")

	vector, err := ctx.openai.Embed(page.Content)
	if err != nil {
		return fmt.Errorf("Failed to vectorize page: %v", err)
	} else {
		log.Printf("vectorized page %s", page.Path)
	}

	return sqlite.Insert(ctx.db, page.Path, page.Stamp, vector)
}

func writePage(ctx *ctx, page *api.Page) error {
	util.Assert(ctx != nil, "writePage nil ctx")
	util.Assert(page != nil, "writePage nil page")

	fullPath := filepath.Join(ctx.config.WikiPath, page.Path+".md")

	// FIXME transactional write+insert
	if err := os.WriteFile(fullPath, []byte(page.Content), 0644); err != nil {
		return fmt.Errorf("Failed to write page: %v", err)
	} else {
		log.Printf("wrote page %s at %s", page.Path, fullPath)
	}

	return index(ctx, page)
}

func searchPages(ctx *ctx, query string) ([]sqlite.SearchResult, error) {
	util.Assert(ctx != nil, "searchPages nil ctx")

	vector, err := ctx.openai.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("failed to vectorize query: %v", err)
	}
	return sqlite.SimilarPages(ctx.db, vector)
}

func wikiHandler(ctx *ctx, w http.ResponseWriter, r *http.Request) {
	// Get the page path from the URL, removing prefix
	prefixLen := len(ctx.config.WikiPrefix)
	pagePath := r.URL.Path[prefixLen:]

	wikiPath0, err := wikiPath(ctx.config)
	if err != nil {
		log.Printf("Failed to get Wiki path: %v", err)
		http.Error(w, "Failed to get Wiki path", http.StatusInternalServerError)
		return
	}
	fullPath := filepath.Join(wikiPath0, pagePath+".md")

	// Read the markdown file
	content, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Failed to read page", http.StatusInternalServerError)
		return
	}

	// Convert markdown to HTML and sanitize output
	md := goldmark.New()
	var buf bytes.Buffer
	if err := md.Convert(content, &buf); err != nil {
		http.Error(w, "Failed to convert markdown", http.StatusInternalServerError)
		return
	}
	html := bluemonday.UGCPolicy().SanitizeBytes(buf.Bytes())

	// Write response
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Render template with content
	tmpl := template.Must(template.New("wiki").Parse(string(data.WikiTemplate)))
	if err := tmpl.Execute(w, struct{ Content string }{string(html)}); err != nil {
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
		return
	}
}

func summarizeSearchResults(ctx *ctx, userQuery string, results []sqlite.SearchResult) (string, openai.Irrelevant, error) {
	util.Assert(ctx != nil, "summarizeSearchResults nil ctx")
	util.Assert(len(results) > 0, "summarizeSearchResults empty results")

	documents := make([]string, 0, len(results))
	for _, result := range results {
		content, err := os.ReadFile(filepath.Join(ctx.config.WikiPath, result.Path+".md"))
		if err != nil {
			return "", nil, fmt.Errorf("failed to read page: %v", err)
		}
		documents = append(documents, string(content))
	}

	return ctx.openai.Summarize(userQuery, documents)
}

func aiHandler(ctx *ctx, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	userQuery := string(body)
	if userQuery == "" {
		http.Error(w, "Empty query", http.StatusBadRequest)
		return
	}

	gptResp, err := ctx.openai.DefaultAskGPT(userQuery)
	if err != nil {
		http.Error(w, "Failed to process query", http.StatusInternalServerError)
		return
	}

	aiResponse, err := openai.ParseResponse(gptResp)
	if err != nil {
		http.Error(w, "Failed to parse AI response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	response := api.PostResponse{}

	// Prepare JSON response
	switch aiResponse.Kind {
	case openai.KindPage:
		page, err := openai.PageOfResponse(aiResponse)
		if err != nil {
			log.Printf("Failed to parse page: %v", err)
			http.Error(w, "Failed to parse page AI response", http.StatusInternalServerError)
			return
		}
		if err := writePage(ctx, page); err != nil {
			log.Printf("Failed to write page %s: %v", page.Path, err)
			http.Error(w, "Failed to write page", http.StatusInternalServerError)
			return
		}
		response.Message = "I saved a new note for you."
		response.ReferencePrefix = ctx.config.WikiPrefix
		response.References = []string{page.Path}
	case openai.KindSearch:
		log.Printf("search query: %s", aiResponse.Content)
		pages, err := searchPages(ctx, aiResponse.Content)
		if err != nil {
			log.Printf("Failed to search pages: %v", err)
			http.Error(w, "Failed to search pages", http.StatusInternalServerError)
			return
		}
		if len(pages) == 0 {
			response.Message = "I found no notes for you."
		} else {
			summary, irrelevant, err := summarizeSearchResults(ctx, userQuery, pages)
			if err != nil {
				log.Printf("Failed to summarize search results: %v", err)
				response.Message = "I found some notes for you."
			} else {
				response.Message = summary
			}
			response.ReferencePrefix = ctx.config.WikiPrefix
			response.References = make([]string, 0, len(pages))
			for i, page := range pages {
				if _, ok := irrelevant[i]; !ok {
					response.References = append(response.References, page.Path)
				}
			}
		}
	case openai.KindUnknown:
		response.Message = aiResponse.Content
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

func handlerWith[T interface{}](t T, fn func(T, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fn(t, w, r)
	}
}

func installHandlers(ctx *ctx) {
	util.Assert(ctx != nil, "installHandlers nil ctx")

	// Serve index.html at the root
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			log.Printf("serving index.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data.IndexHTML)
			return
		}
		log.Printf("not found: %s", r.URL.Path)
		http.NotFound(w, r)
		return
	})

	http.HandleFunc(api.PostPath, handlerWith(ctx, aiHandler))
	http.HandleFunc(ctx.config.WikiPrefix+"/", handlerWith(ctx, wikiHandler))

	// Serve style.css
	http.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("serving style.css")
		w.Header().Set("Content-Type", "text/css")
		w.Write(data.StyleCSS)
	})
}

func initSqlite(config *config) *sql.DB {
	util.Assert(config != nil, "initSqlite nil config")

	wikiPath, err := wikiPath(config)
	if err != nil {
		log.Fatal("Failed to get Wiki path:", err)
	}

	dbPath := filepath.Join(wikiPath, "sqlite")
	return sqlite.Init(dbPath)
}

func PathsPhysicallyEqual(p1, p2 string) bool {
	info1, err1 := os.Stat(p1)
	info2, err2 := os.Stat(p2)

	if err1 != nil || err2 != nil {
		log.Printf("Error statting paths: %v, %v", err1, err2)
		return false
	}

	return os.SameFile(info1, info2)
}

func validateAndIndex(ctx *ctx, path string) error {
	util.Assert(ctx != nil, "validateAndIndex nil ctx")

	pagePath := strings.TrimSuffix(filepath.Base(path), ".md")

	if err := util.ValidatePagePath(pagePath); err != nil {
		return fmt.Errorf("invalid page path: %w", err)
	}

	wikiPath0, err := wikiPath(ctx.config)
	if err != nil {
		return fmt.Errorf("failed to get wiki path: %w", err)
	}

	path2 := filepath.Join(wikiPath0, pagePath+".md")
	if !PathsPhysicallyEqual(path, path2) {
		return fmt.Errorf("path %s is outside managed directory", path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read page %s: %w", path, err)
	}

	page := api.Page{
		Path:    pagePath,
		Content: string(content),
	}

	err = index(ctx, &page)
	if err != nil {
		return fmt.Errorf("failed to index page %s: %w", pagePath, err)
	}

	return nil
}

// This is in the server module because it needs almost the same context. No
// need to keep it in the server process though. If another process
// simultaneously indexes the same files the worst thing that can happen is
// non-deterministic SQLite3 INSERT.
func Index(paths []string) {
	ctx := newCtx()
	defer ctx.Close()

	for _, path := range paths {
		if err := validateAndIndex(ctx, path); err != nil {
			log.Printf("failed to index page %s: %v", path, err)
		}
	}
}

func Main() {
	ctx := newCtx()
	defer ctx.Close()

	installHandlers(ctx)

	log.Printf("Server starting on port %d...", ctx.config.Port)
	http.ListenAndServe(fmt.Sprintf(":%d", ctx.config.Port), nil)
}
