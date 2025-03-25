package openai

import (
	"context"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/vasilisp/wikai/internal/data"
	"github.com/vasilisp/wikai/internal/util"
)

type Client struct {
	client              *openai.Client
	embeddingDimensions int
}

func NewClient(token string, embeddingDimensions int) *Client {
	util.Assert(token != "", "NewClient empty token")
	util.Assert(embeddingDimensions > 0, "NewClient non-positive embeddingDimensions")

	client := openai.NewClient(option.WithAPIKey(token))
	util.Assert(client != nil, "NewClient nil client")

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

func (c *Client) Embed(str string) ([]float32, error) {
	util.Assert(c != nil, "embed nil client")
	util.Assert(str != "", "embed empty string")

	// Create embedding request

	strings := *splitTextIntoChunks(str, 512)

	inputUnion := openai.EmbeddingNewParamsInputUnion(openai.EmbeddingNewParamsInputArrayOfStrings(strings))
	embedding, err := c.client.Embeddings.New(context.TODO(), openai.EmbeddingNewParams{
		Input:      openai.F(inputUnion),
		Model:      openai.F(openai.EmbeddingModelTextEmbedding3Small),
		Dimensions: openai.F(int64(c.embeddingDimensions)),
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

func (c *Client) AskGPT(systemMessage string, userMessage string) (string, error) {
	util.Assert(c != nil, "AskGPT nil client")
	util.Assert(systemMessage != "", "AskGPT empty systemMessage")
	util.Assert(userMessage != "", "AskGPT empty userMessage")

	chatCompletion, err := c.client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
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

func (c *Client) DefaultAskGPT(userMessage string) (string, error) {
	return c.AskGPT(data.SystemPrompt, userMessage)
}
