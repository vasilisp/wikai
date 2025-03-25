package main

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
	"github.com/microcosm-cc/bluemonday"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/yuin/goldmark"
)

func assert(condition bool, msg string) {
	if !condition {
		log.Fatalf("Assertion failed: %s", msg)
	}
}

type Config struct {
	WikiPath            string `json:"wikiPath"`
	WikiPrefix          string `json:"wikiPrefix,omitempty"`
	OpenAIToken         string `json:"openaiToken"`
	EmbeddingDimensions int    `json:"embeddingDimensions,omitempty"`
}

type Ctx struct {
	config *Config
	db     *sql.DB
}

func newCtx(config *Config, db *sql.DB) Ctx {
	assert(config != nil, "newCtx nil config")
	assert(db != nil, "newCtx nil DB")

	return Ctx{
		config: config,
		db:     db,
	}
}

func loadConfig() *Config {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatal("Failed to get home directory:", err)
	}

	configPath := filepath.Join(homeDir, ".config", "wikai.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatal("Failed to read config file:", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		log.Fatal("Failed to parse config file:", err)
	}

	// Set default wiki prefix if not specified
	if config.WikiPrefix == "" {
		config.WikiPrefix = "/wikai"
	}

	return &config
}

func handler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Hello, World!")
}

func writePage(ctx Ctx, page *page) error {
	fullPath := filepath.Join(ctx.config.WikiPath, page.path+".md")

	// FIXME transactional write+insert
	if err := os.WriteFile(fullPath, []byte(page.content), 0644); err != nil {
		return fmt.Errorf("Failed to write page: %v", err)
	} else {
		log.Printf("wrote page %s at %s", page.path, fullPath)
	}

	vector, err := vectorizePage(ctx.config, page)
	if err != nil {
		return fmt.Errorf("Failed to vectorize page: %v", err)
	} else {
		log.Printf("vectorized page %s", page.path)
	}

	blob, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return fmt.Errorf("Failed to serialize vector: %v", err)
	}

	// Insert into SQLite DB
	if _, err := ctx.db.Exec(`
			INSERT INTO embeddings(path, created_at, embedding)
			VALUES (?, ?, ?)
			ON CONFLICT(path) DO NOTHING
		    `, page.path, page.stamp, blob); err != nil {
		return fmt.Errorf("Failed to update database: %v", err)
	} else {
		log.Printf("updated database for page %s", page.path)
	}

	return nil
}

func similarPages(ctx Ctx, vector []float32) ([]string, error) {
	blob, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize vector: %v", err)
	}

	rows, err := ctx.db.Query(`
		SELECT embeddings.path
		FROM embeddings
		ORDER BY vec_distance_cosine(embedding, ?) ASC
		LIMIT 5
	`, blob)
	if err != nil {
		return nil, fmt.Errorf("similarPages query error: %v", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("similarPages scan error: %v", err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func searchPages(ctx Ctx, query string) ([]string, error) {
	vector, err := vectorizeString(ctx.config, query)
	if err != nil {
		return nil, fmt.Errorf("failed to vectorize query: %v", err)
	}
	return similarPages(ctx, vector)
}

func wikiPath(config *Config) (string, error) {
	wikiPath := config.WikiPath
	if wikiPath[:2] == "~/" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("Failed to get home directory: %v", err)
		}
		wikiPath = filepath.Join(homeDir, wikiPath[2:])
	}
	return wikiPath, nil
}

func wikiHandler(config *Config, w http.ResponseWriter, r *http.Request) {
	// Get the page path from the URL, removing prefix
	prefixLen := len(config.WikiPrefix)
	pagePath := r.URL.Path[prefixLen:]

	wikiPath, err := wikiPath(config)
	if err != nil {
		log.Printf("Failed to get Wiki path: %v", err)
		http.Error(w, "Failed to get Wiki path", http.StatusInternalServerError)
		return
	}
	fullPath := filepath.Join(wikiPath, pagePath+".md")

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

func openaiClient(config *Config) *openai.Client {
	assert(config != nil, "openaiClient nil config")
	assert(config.OpenAIToken != "", "openaiClient non-empty OpenAIToken")
	return openai.NewClient(
		option.WithAPIKey(config.OpenAIToken),
	)
}

func askGPT(config *Config, systemMessage string, userMessage string) (string, error) {
	if config.OpenAIToken == "" {
		return "", fmt.Errorf("OpenAI token not configured")
	}

	client := openaiClient(config)
	assert(client != nil, "askGPT nil openaiClient")

	chatCompletion, err := client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
		Messages: openai.F([]openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemMessage),
			openai.UserMessage(userMessage),
		}),
		Model: openai.F(openai.ChatModelGPT4o),
	})
	if err != nil {
		return "", fmt.Errorf("ChatCompletion error: %v", err)
	}

	return chatCompletion.Choices[0].Message.Content, nil
}

//go:embed prompt.txt
var systemPrompt string

func defaultAskGPT(config *Config, query string) (string, error) {
	return askGPT(config, systemPrompt, query)
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
	assert(path != "", "newPageResponse non-empty path")
	assert(stamp != 0, "newPageResponse non-zero stamp")

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

func splitTextIntoChunks(text string, chunkSize int) *[]string {
	var chunks []string
	runes := []rune(text) // Handle multi-byte characters
	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	return &chunks
}

func vectorizeString(config *Config, str string) ([]float32, error) {
	assert(config != nil, "vectorizeString nil config")
	assert(str != "", "vectorizeString empty string")

	// Create embedding request
	client := openaiClient(config)
	assert(client != nil, "vectorizePage nil openaiClient")

	strings := *splitTextIntoChunks(str, 512)

	inputUnion := openai.EmbeddingNewParamsInputUnion(openai.EmbeddingNewParamsInputArrayOfStrings(strings))
	embeddingDimensions := int64(config.EmbeddingDimensions)
	if embeddingDimensions <= 0 {
		embeddingDimensions = 1536
	}
	assert(embeddingDimensions > 0, "vectorizePage non-positive embeddingDimensions")
	embedding, err := client.Embeddings.New(context.TODO(), openai.EmbeddingNewParams{
		Input:      openai.F(inputUnion),
		Model:      openai.F(openai.EmbeddingModelTextEmbedding3Small),
		Dimensions: openai.F(embeddingDimensions),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create embedding: %v", err)
	}

	if len(embedding.Data) == 0 {
		return nil, fmt.Errorf("no embedding data returned")
	}

	vector := embedding.Data[0].Embedding
	vectorFloat32 := make([]float32, len(vector))
	for i, v := range vector {
		vectorFloat32[i] = float32(v)
	}

	return vectorFloat32, nil
}

func vectorizePage(config *Config, page *page) ([]float32, error) {
	assert(config != nil, "vectorizePage nil config")
	assert(page != nil, "vectorizePage nil page")
	return vectorizeString(config, page.content)
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

	return &aiResponseRaw{kv: result, content: parts[2]}, nil
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

func aiHandler(ctx Ctx, w http.ResponseWriter, r *http.Request) {
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

	gptResp, err := defaultAskGPT(ctx.config, query)
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

func chatHandler(config *Config, w http.ResponseWriter, r *http.Request) {
	_ = r
	resp, err := defaultAskGPT(config, "What do I have to do today?")
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

func installHandlers(ctx Ctx) {
	assert(ctx.config != nil, "installHandlers nil config")
	http.HandleFunc("/", handler)
	http.HandleFunc("/chat", handlerWith(ctx.config, chatHandler))
	http.HandleFunc("/ai", handlerWith(ctx, aiHandler))
	http.HandleFunc("/wiki/", handlerWith(ctx.config, wikiHandler))
}

func sqliteVecVersion(db *sql.DB) (string, error) {
	var vecVersion string
	err := db.QueryRow("select vec_version()").Scan(&vecVersion)
	if err != nil {
		return "", err
	}
	return vecVersion, nil
}

func initSqlite(config *Config) *sql.DB {
	assert(config != nil, "initSqlite nil config")

	sqlite_vec.Auto()

	wikiPath, err := wikiPath(config)
	if err != nil {
		log.Fatal("Failed to get Wiki path:", err)
	}

	dbPath := filepath.Join(wikiPath, "sqlite")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		log.Fatal("Failed to create database directory:", err)
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS embeddings(
			path TEXT NOT NULL UNIQUE,
			embedding BLOB NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS embeddings_path ON embeddings(path);
	`)
	if err != nil {
		log.Fatalf("failed to create tables: %v", err)
	}

	vecVersion, err := sqliteVecVersion(db)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("sqlite_vec version %s\n", vecVersion)
	return db
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

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "cli" {
		cliAskGPT(os.Args[2:])
		return
	}

	mainServer()
}
