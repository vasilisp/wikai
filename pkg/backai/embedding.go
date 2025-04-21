package backai

import (
	"context"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/vasilisp/wikai/internal/util"
)

type EmbeddingClient struct {
	client              *openai.Client
	embeddingDimensions int
}

func NewEmbeddingClient(token string, embeddingDimensions int) EmbeddingClient {
	util.Assert(token != "", "NewClient empty token")
	util.Assert(embeddingDimensions > 0, "NewClient non-positive embeddingDimensions")

	client := openai.NewClient(option.WithAPIKey(token))

	return EmbeddingClient{
		client:              &client,
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

func (c EmbeddingClient) Embed(str string) ([]float64, error) {
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
