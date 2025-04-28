package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/vasilisp/wikai/internal/api"
	"github.com/vasilisp/wikai/internal/data"
	"github.com/vasilisp/wikai/internal/git"
	"github.com/vasilisp/wikai/internal/util"
	"github.com/vasilisp/wikai/pkg/backai"
	"github.com/vasilisp/wikai/pkg/embedding"
	"github.com/yuin/goldmark"
)

type ctx struct {
	config *config
	git    git.Repo
	bai    *backai.Ctx
}

func loadEmbeddings(ctx *ctx) error {
	util.Assert(ctx != nil, "loadEmbeddings nil ctx")
	start := time.Now()

	err := ctx.git.GetNoteContents(func(embJSON string) {
		var emb embedding.Embedding
		if err := json.Unmarshal([]byte(embJSON), &emb); err != nil {
			log.Printf("failed to unmarshal embedding: %v", err)
		}
		ctx.bai.DB().Add(emb.ID, emb.Vector, emb.Stamp)
	})
	if err != nil {
		return fmt.Errorf("failed to get note contents: %w", err)
	}

	log.Printf("loaded %d embeddings in %.2f seconds", ctx.bai.DB().NumRows(), time.Since(start).Seconds())

	return nil
}

func newCtx() *ctx {
	config := loadConfig()
	util.Assert(config != nil, "newCtx nil config")

	git, err := git.NewRepo(config.WikiPath, "")
	util.Assert(err == nil, "newCtx failed to create git repo")

	ctx := ctx{
		config: config,
		git:    git,
	}

	ctx.bai = backai.NewCtx(&ctx, ctx.config.WikiPrefix, ctx.config.OpenAIToken, ctx.config.EmbeddingDimensions)

	return &ctx
}

func index(ctx *ctx, path, content string, vector []float64) error {
	util.Assert(ctx != nil, "index nil ctx")
	util.Assert(path != "", "index empty path")
	util.Assert(content != "", "index empty content")
	util.Assert(vector != nil, "index nil vector")
	util.Assert(len(vector) > 0, "index empty vector")

	err := ctx.git.Add(path + ".md")
	if err != nil {
		return fmt.Errorf("Failed to add page to git: %v", err)
	}

	emb := embedding.Embedding{
		ID:     path,
		Vector: vector,
		Stamp:  time.Now(),
	}
	embJSON, err := json.Marshal(emb)
	if err != nil {
		return fmt.Errorf("Failed to marshal embedding: %v", err)
	}

	err = ctx.git.Commit(fmt.Sprintf("Add %s", path), true)
	if err != nil {
		return fmt.Errorf("Failed to commit page to git: %v", err)
	}

	err = ctx.git.AddNote(string(embJSON))
	if err != nil {
		return fmt.Errorf("Failed to add vector to git: %v", err)
	}

	return nil
}

func wikiHandler(ctx *ctx, w http.ResponseWriter, r *http.Request) {
	// Get the page path from the URL, removing prefix
	prefixLen := len(ctx.config.WikiPrefix)
	util.Assert(len(r.URL.Path) >= prefixLen+2, "wikiHandler empty page path")
	pagePath := r.URL.Path[prefixLen+1:]

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
	docStampStr := "unknown"
	docStamp, ok := ctx.bai.DB().DocStamp(pagePath)
	if ok {
		docStampStr = docStamp.Format("2006-01-02 15:04:05")
	}

	if err := tmpl.Execute(w, struct {
		Content string
		Stamp   string
	}{string(html), docStampStr}); err != nil {
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
		return
	}
}

func (ctx *ctx) Read(path string) (string, error) {
	content, err := os.ReadFile(filepath.Join(ctx.config.WikiPath, path+".md"))
	if err != nil {
		return "", fmt.Errorf("failed to read page: %v", err)
	}
	return string(content), nil
}

func (ctx *ctx) Write(path string, content string, embedding []float64) error {
	util.Assert(ctx != nil, "writePage nil ctx")
	util.Assert(path != "", "writePage empty path")
	util.Assert(content != "", "writePage empty content")

	fullPath := filepath.Join(ctx.config.WikiPath, path+".md")

	// FIXME transactional write+insert
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("Failed to write page: %v", err)
	} else {
		log.Printf("wrote page %s at %s", path, fullPath)
	}

	return index(ctx, path, content, embedding)
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

	var postRequest api.PostRequest
	if err := json.Unmarshal(body, &postRequest); err != nil {
		http.Error(w, "Failed to parse request body", http.StatusBadRequest)
		return
	}

	userQuery := postRequest.Message
	if userQuery == "" {
		http.Error(w, "Empty query", http.StatusBadRequest)
		return
	}

	aiResponse, err := ctx.bai.Query(userQuery, postRequest.ChatID)
	if err != nil {
		log.Printf("LLM error: %v", err)
		http.Error(w, "LLM error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	json.NewEncoder(w).Encode(aiResponse)
}

func validateAndIndex(ctx *ctx, path string) error {
	util.Assert(ctx != nil, "validateAndIndex nil ctx")

	path = strings.TrimSuffix(path, ".md")

	if err := util.ValidatePagePath(path); err != nil {
		return fmt.Errorf("invalid page path: %w", err)
	}

	wikiPath0, err := wikiPath(ctx.config)
	if err != nil {
		return fmt.Errorf("failed to get wiki path: %w", err)
	}
	fullPath := filepath.Join(wikiPath0, path+".md")

	content, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("failed to read page %s: %w", path, err)
	}

	embedding, err := ctx.bai.Embed(string(content))
	if err != nil {
		return fmt.Errorf("failed to embed page %s: %w", path, err)
	}

	err = index(ctx, path, string(content), embedding)
	if err != nil {
		return fmt.Errorf("failed to index page %s: %w", path, err)
	}

	return nil
}

func indexHandler(ctx *ctx, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	paths := strings.Split(string(body), "\n")
	for _, path := range paths {
		if err := validateAndIndex(ctx, path); err != nil {
			log.Printf("failed to index page %s: %v", path, err)
			http.Error(w, "Failed to index page", http.StatusInternalServerError)
			return
		}
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
	http.HandleFunc(api.IndexPath, handlerWith(ctx, indexHandler))
	http.HandleFunc(ctx.config.WikiPrefix+"/", handlerWith(ctx, wikiHandler))

	// Serve style.css
	http.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("serving style.css")
		w.Header().Set("Content-Type", "text/css")
		w.Write(data.StyleCSS)
	})
}

func Main() {
	ctx := newCtx()

	err := loadEmbeddings(ctx)
	if err != nil {
		log.Printf("failed to load embeddings: %v", err)
		os.Exit(1)
	}

	installHandlers(ctx)

	log.Printf("Server starting on port %d...", ctx.config.Port)
	http.ListenAndServe(fmt.Sprintf(":%d", ctx.config.Port), nil)
}
