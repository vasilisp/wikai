package openai

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/vasilisp/wikai/internal/api"
	"github.com/vasilisp/wikai/internal/data"
	"github.com/vasilisp/wikai/internal/util"
)

type Client struct {
	client              openai.Client
	embeddingDimensions int
}

func NewClient(token string, embeddingDimensions int) *Client {
	util.Assert(token != "", "NewClient empty token")
	util.Assert(embeddingDimensions > 0, "NewClient non-positive embeddingDimensions")

	client := openai.NewClient(option.WithAPIKey(token))

	return &Client{
		client:              client,
		embeddingDimensions: embeddingDimensions,
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

func (c *Client) Embed(str string) ([]float64, error) {
	util.Assert(c != nil, "embed nil client")
	util.Assert(str != "", "embed empty string")

	// Create embedding request

	strings := *splitTextIntoChunks(str, 512)

	embedding, err := c.client.Embeddings.New(context.TODO(), openai.EmbeddingNewParams{
		Input:      openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: strings},
		Model:      openai.EmbeddingModelTextEmbedding3Small,
		Dimensions: openai.Opt(int64(c.embeddingDimensions)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create embedding: %v", err)
	}

	if len(embedding.Data) == 0 {
		return nil, fmt.Errorf("no embedding data returned")
	}

	vector := embedding.Data[0].Embedding

	return vector, nil
}

func extractGPTResponse(chatCompletion *openai.ChatCompletion) (string, error) {
	if chatCompletion == nil {
		return "", fmt.Errorf("nil chatCompletion")
	}
	if len(chatCompletion.Choices) == 0 {
		return "", fmt.Errorf("no choices returned")
	}
	return chatCompletion.Choices[0].Message.Content, nil
}

func (c *Client) AskGPT(systemMessage string, userMessage string) (string, error) {
	util.Assert(c != nil, "AskGPT nil client")
	util.Assert(systemMessage != "", "AskGPT empty systemMessage")
	util.Assert(userMessage != "", "AskGPT empty userMessage")

	chatCompletion, err := c.client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemMessage),
			openai.UserMessage(userMessage),
		},
		Model: openai.ChatModelGPT4o,
	})
	if err != nil {
		return "", fmt.Errorf("ChatCompletion error: %v", err)
	}

	return extractGPTResponse(chatCompletion)
}

func (c *Client) DefaultAskGPT(userMessage string) (string, error) {
	return c.AskGPT(data.SystemPrompt, userMessage)
}

type ResponseKind int

const (
	KindUnknown ResponseKind = iota
	KindPage
	KindSearch
)

type Response struct {
	Kind    ResponseKind
	KV      map[string]string
	Content string
}

func responseKind(kv map[string]string) ResponseKind {
	kind, ok := kv["type"]
	if !ok {
		return KindUnknown
	}

	switch kind {
	case "newpage":
		return KindPage
	case "search":
		return KindSearch
	default:
		return KindUnknown
	}
}

func splitFrontMatter(response string) (string, map[string]string) {
	parts := strings.Split(response, "---")
	if len(parts) < 3 {
		return response, nil
	}

	frontMatter := parts[1]
	lines := strings.Split(strings.TrimSpace(frontMatter), "\n")

	kv := make(map[string]string)
	for _, line := range lines {
		kvPair := strings.Split(line, ":")
		if len(kvPair) != 2 {
			continue
		}
		key := strings.TrimSpace(kvPair[0])
		value := strings.TrimSpace(kvPair[1])
		kv[key] = value
	}

	return strings.TrimLeft(parts[2], " \t\n\r"), kv
}

func ParseResponse(response string) (*Response, error) {
	content, kv := splitFrontMatter(response)

	kind := responseKind(kv)
	delete(kv, "type")

	return &Response{Kind: kind, KV: kv, Content: content}, nil
}

func PageOfResponse(response *Response) (*api.Page, error) {
	util.Assert(response != nil, "pageOfResponse nil response")
	util.Assert(response.Kind == KindPage, "pageOfResponse not a page")

	// Parse timestamp
	stamp, err := strconv.ParseInt(response.KV["stamp"], 10, 64)
	if err != nil || stamp == 0 {
		return nil, fmt.Errorf("invalid timestamp: %v", err)
	}

	// Extract title from content
	lines := strings.Split(strings.TrimSpace(response.Content), "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("content is empty")
	}
	title := strings.TrimSpace(strings.TrimPrefix(lines[0], "#"))

	path, ok := response.KV["path"]
	if !ok || path == "" {
		return nil, fmt.Errorf("missing path field")
	}

	if err := util.ValidatePagePath(path); err != nil {
		return nil, fmt.Errorf("invalid path: %v", err)
	}

	return &api.Page{
		Title:   title,
		Content: response.Content,
		Path:    path,
		Stamp:   stamp,
	}, nil
}

// read-only: can be nil
type Irrelevant map[int]struct{}

func (c *Client) Summarize(userQuery string, documents []string) (string, Irrelevant, error) {
	util.Assert(c != nil, "Summarize nil client")
	util.Assert(len(documents) > 0, "Summarize empty documents")

	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(documents)+1)

	systemMessage := data.SystemPromptSummarize
	messages = append(messages, openai.SystemMessage(systemMessage))

	if userQuery != "" {
		messages = append(messages, openai.UserMessage(fmt.Sprintf("User query:\n\n%s", userQuery)))
	}

	for i, document := range documents {
		messages = append(messages, openai.UserMessage(fmt.Sprintf("Document %d:\n\n%s", i+1, document)))
	}

	chatCompletion, err := c.client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
		Messages: messages,
		Model:    openai.ChatModelGPT4o,
	})
	if err != nil {
		return "", nil, fmt.Errorf("ChatCompletion error: %v", err)
	}

	gptResponse, err := extractGPTResponse(chatCompletion)
	if err != nil {
		return "", nil, fmt.Errorf("extractGPTResponse error: %v", err)
	}

	summary, kv := splitFrontMatter(gptResponse)

	if kv["irrelevant"] != "" {
		irrelevant := make(map[int]struct{})
		for _, index := range strings.Split(kv["irrelevant"], ",") {
			idx, err := strconv.Atoi(strings.TrimSpace(index))
			if err != nil {
				log.Printf("invalid irrelevant: %v", err)
			} else {
				// documents are 1-indexed
				irrelevant[idx-1] = struct{}{}
			}
		}
		return summary, Irrelevant(irrelevant), nil
	}
	return summary, nil, nil
}
