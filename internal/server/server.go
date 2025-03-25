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
	"strconv"
	"strings"

	"github.com/microcosm-cc/bluemonday"
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

func newCtx(config *config, db *sql.DB) *ctx {
	util.Assert(config != nil, "newCtx nil config")
	util.Assert(db != nil, "newCtx nil DB")

	openai := openai.NewClient(config.OpenAIToken, config.EmbeddingDimensions)

	return &ctx{
		config: config,
		db:     db,
		openai: openai,
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Hello, World!")
}

func writePage(ctx *ctx, page *page) error {
	fullPath := filepath.Join(ctx.config.WikiPath, page.path+".md")

	// FIXME transactional write+insert
	if err := os.WriteFile(fullPath, []byte(page.content), 0644); err != nil {
		return fmt.Errorf("Failed to write page: %v", err)
	} else {
		log.Printf("wrote page %s at %s", page.path, fullPath)
	}

	vector, err := ctx.openai.Embed(page.content)
	if err != nil {
		return fmt.Errorf("Failed to vectorize page: %v", err)
	} else {
		log.Printf("vectorized page %s", page.path)
	}

	return sqlite.Insert(ctx.db, page.path, page.stamp, vector)
}

func searchPages(ctx *ctx, query string) ([]string, error) {
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
	w.Write(html)
}

type aiResponseRaw struct {
	kv      map[string]string
	content string
}

type aiResponseKind int

const (
	kindRaw aiResponseKind = iota
	kindPage
	kindSearch
)

type page struct {
	title   string
	content string
	path    string
	stamp   int64
}

// FIXME: ugly wasteful sum type encoding; use interfaces
type aiResponse struct {
	kind   aiResponseKind
	page   *page          // used if kind == kindPage
	raw    *aiResponseRaw // used if kind == kindRaw
	search string         // used if kind == kindSearch
}

type postResponse struct {
	Response string `json:"response"`
}

func newRawResponse(resp *aiResponseRaw) *aiResponse {
	return &aiResponse{
		kind: kindRaw,
		raw:  resp,
	}
}

func newPageResponse(title, content, path string, stamp int64) *aiResponse {
	util.Assert(path != "", "newPageResponse non-empty path")
	util.Assert(stamp != 0, "newPageResponse non-zero stamp")

	return &aiResponse{
		kind: kindPage,
		page: &page{
			title:   title,
			content: content,
			path:    path,
			stamp:   stamp,
		},
	}
}

func newSearchResponse(query string) *aiResponse {
	return &aiResponse{
		kind:   kindSearch,
		search: query,
	}
}

func parseAIResponseRaw(response string) (*aiResponseRaw, error) {
	// Split response into front matter and content
	parts := strings.Split(response, "---")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid response format: missing front matter")
	}

	// Parse YAML front matter
	frontMatter := parts[1]
	lines := strings.Split(strings.TrimSpace(frontMatter), "\n")

	result := make(map[string]string)
	for _, line := range lines {
		kv := strings.Split(line, ":")
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])
		result[key] = value
	}

	// Ensure type field exists
	if _, exists := result["type"]; !exists {
		return nil, fmt.Errorf("invalid response format: missing type field")
	}

	content := strings.TrimLeft(parts[2], " \t\n\r")
	return &aiResponseRaw{kv: result, content: content}, nil
}

func convertAIResponse(raw *aiResponseRaw) (*aiResponse, error) {
	switch raw.kv["type"] {
	case "newpage":
		// Parse timestamp
		stamp, err := strconv.ParseInt(raw.kv["stamp"], 10, 64)
		if err != nil || stamp == 0 {
			return nil, fmt.Errorf("invalid timestamp: %v", err)
		}

		// Extract title from content
		lines := strings.Split(strings.TrimSpace(raw.content), "\n")
		if len(lines) == 0 {
			return nil, fmt.Errorf("content is empty")
		}
		title := strings.TrimSpace(strings.TrimPrefix(lines[0], "#"))

		path, ok := raw.kv["path"]
		if !ok || path == "" {
			return nil, fmt.Errorf("missing path field")
		}

		return newPageResponse(
			title,
			raw.content,
			path,
			stamp,
		), nil
	case "search":
		return newSearchResponse(raw.content), nil
	default:
		return newRawResponse(raw), nil
	}
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

	query := string(body)
	if query == "" {
		http.Error(w, "Empty query", http.StatusBadRequest)
		return
	}

	gptResp, err := ctx.openai.DefaultAskGPT(query)
	if err != nil {
		http.Error(w, "Failed to process query", http.StatusInternalServerError)
		return
	}

	raw, err := parseAIResponseRaw(gptResp)
	if err != nil {
		http.Error(w, "Failed to parse AI response", http.StatusInternalServerError)
		return
	}

	aiResponse, err := convertAIResponse(raw)
	if err != nil {
		http.Error(w, "Failed to convert AI response", http.StatusInternalServerError)
		return
	}

	resp := ""

	// Prepare JSON response
	switch aiResponse.kind {
	case kindPage:
		if err := writePage(ctx, aiResponse.page); err != nil {
			log.Printf("Failed to write page %s: %v", aiResponse.page.path, err)
			http.Error(w, "Failed to write page", http.StatusInternalServerError)
			return
		}
		resp = fmt.Sprintf("saved page %s", aiResponse.page.path)
	case kindSearch:
		log.Printf("search query: %s", aiResponse.search)
		pages, err := searchPages(ctx, aiResponse.search)
		if err != nil {
			log.Printf("Failed to search pages: %v", err)
			http.Error(w, "Failed to search pages", http.StatusInternalServerError)
			return
		}
		resp = fmt.Sprintf("search results: %v", pages)
	case kindRaw:
		resp = aiResponse.raw.content
	}

	w.Header().Set("Content-Type", "application/json")

	response := postResponse{Response: resp}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

func chatHandler(ctx *ctx, w http.ResponseWriter, r *http.Request) {
	_ = r
	resp, err := ctx.openai.DefaultAskGPT("What do I have to do today?")
	if err != nil {
		http.Error(w, "Failed to ask GPT", http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s", resp)
}

func cliAskGPT(args []string) {
	var query string

	if len(args) == 0 {
		// Read from stdin
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			log.Fatal("Failed to read from stdin:", err)
		}
		query = string(input)
	} else {
		query = strings.Join(args, " ")
	}

	// Create HTTP client
	client := &http.Client{}

	// Create request
	req, err := http.NewRequest("POST", "http://localhost:8080/ai", strings.NewReader(query))
	if err != nil {
		log.Fatal("Failed to create request:", err)
	}

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal("Failed to send request:", err)
	}
	defer resp.Body.Close()

	// Read response
	var result postResponse

	if resp.StatusCode != http.StatusOK {
		log.Printf("Failed to get response: %s", resp.Status)
		os.Exit(1)
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Fatal("Failed to decode response:", err)
	}

	fmt.Println(result.Response)
}

func handlerWith[T interface{}](t T, fn func(T, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fn(t, w, r)
	}
}

func installHandlers(ctx *ctx) {
	util.Assert(ctx != nil, "installHandlers nil ctx")
	http.HandleFunc("/", handler)
	http.HandleFunc("/chat", handlerWith(ctx, chatHandler))
	http.HandleFunc("/ai", handlerWith(ctx, aiHandler))
	http.HandleFunc("/wiki/", handlerWith(ctx, wikiHandler))
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

func mainServer() {
	config := loadConfig()

	db := initSqlite(config)
	defer db.Close()
	ctx := newCtx(config, db)

	installHandlers(ctx)

	log.Println("Server starting on port 8080...")
	http.ListenAndServe(":8080", nil)
}

func Main() {
	if len(os.Args) >= 2 && os.Args[1] == "cli" {
		cliAskGPT(os.Args[2:])
		return
	}

	mainServer()
}
