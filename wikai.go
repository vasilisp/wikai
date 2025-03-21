package main

import (
	"bytes"
	"context"
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
	WikiPath    string `json:"wikiPath"`
	WikiPrefix  string `json:"wikiPrefix,omitempty"`
	OpenAIToken string `json:"openaiToken"`
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

func writeWiki(config *Config, pagePath string, content string) error {
	fullPath := filepath.Join(config.WikiPath, pagePath+".md")
	return os.WriteFile(fullPath, []byte(content), 0644)
}

func wikiHandler(config *Config, w http.ResponseWriter, r *http.Request) {
	// Get the page path from the URL, removing prefix
	prefixLen := len(config.WikiPrefix)
	pagePath := r.URL.Path[prefixLen:]

	// Expand ~ to home directory in wiki path
	wikiPath := config.WikiPath
	if wikiPath[:2] == "~/" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			http.Error(w, "Failed to get home directory", http.StatusInternalServerError)
			return
		}
		wikiPath = filepath.Join(homeDir, wikiPath[2:])
	}

	// Get full path to markdown file
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
	assert(config != nil, "openaiClient non-nil config")
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
	assert(client != nil, "askGPT non-nil openaiClient")

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
)

type page struct {
	title   string
	content string
	path    string
	stamp   int64
}

type aiResponse struct {
	kind aiResponseKind
	page *page          // used if kind == kindPage
	raw  *aiResponseRaw // used if kind == kindRaw
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

func vectorizePage(config *Config, page *page) ([]float64, error) {
	assert(config != nil, "vectorizePage nil config")
	assert(page != nil, "vectorizePage nil page")

	// Create embedding request
	client := openaiClient(config)
	assert(client != nil, "vectorizePage non-nil openaiClient")

	strings := *splitTextIntoChunks(page.content, 512)

	inputUnion := openai.EmbeddingNewParamsInputUnion(openai.EmbeddingNewParamsInputArrayOfStrings(strings))
	embedding, err := client.Embeddings.New(context.TODO(), openai.EmbeddingNewParams{
		Input: openai.F(inputUnion),
		Model: openai.F(openai.EmbeddingModelTextEmbedding3Small),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create embedding: %v", err)
	}

	if len(embedding.Data) == 0 {
		return nil, fmt.Errorf("no embedding data returned")
	}

	return embedding.Data[0].Embedding, nil
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
	if raw.kv["type"] == "newpage" {
		// Parse timestamp
		stamp, err := strconv.ParseInt(raw.kv["stamp"], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid timestamp: %v", err)
		}

		// Extract title from content
		lines := strings.Split(strings.TrimSpace(raw.content), "\n")
		if len(lines) == 0 {
			return nil, fmt.Errorf("content is empty")
		}
		title := strings.TrimSpace(strings.TrimPrefix(lines[0], "#"))

		path, ok := raw.kv["path"]
		if !ok {
			return nil, fmt.Errorf("missing path field")
		}

		return newPageResponse(
			title,
			raw.content,
			path,
			stamp,
		), nil
	}

	return newRawResponse(raw), nil
}

func aiHandler(config *Config, w http.ResponseWriter, r *http.Request) {
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

	gptResp, err := defaultAskGPT(config, query)
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
	if aiResponse.kind == kindPage {
		writeWiki(config, aiResponse.page.path, aiResponse.page.content)
		vector, err := vectorizePage(config, aiResponse.page)
		if err != nil {
			http.Error(w, "Failed to vectorize page", http.StatusInternalServerError)
			return
		}
		resp = fmt.Sprintf("saved vector of length %d for %s", len(vector), aiResponse.page.path)
	} else {
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
			fmt.Println("Failed to read from stdin")
			os.Exit(1)
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
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Fatal("Failed to decode response:", err)
	}

	fmt.Println(result.Response)
}

func handlerWithConfig(config *Config, fn func(*Config, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fn(config, w, r)
	}
}

func installHandlers(config *Config) {
	assert(config != nil, "installHandlers nil config")
	http.HandleFunc("/", handler)
	http.HandleFunc("/chat", handlerWithConfig(config, chatHandler))
	http.HandleFunc("/ai", handlerWithConfig(config, aiHandler))
	http.HandleFunc("/wiki/", handlerWithConfig(config, wikiHandler))
}

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "cli" {
		cliAskGPT(os.Args[2:])
		return
	}
	config := loadConfig()
	installHandlers(config)
	fmt.Println("Server starting on port 8080...")
	http.ListenAndServe(":8080", nil)
}
