// general AI backend functionality

package backai

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
	"unicode"

	"github.com/golang/groupcache/lru"
	"github.com/google/uuid"
	"github.com/vasilisp/lingograph"
	"github.com/vasilisp/lingograph/openai"
	"github.com/vasilisp/lingograph/store"
	"github.com/vasilisp/wikai/internal/data"
	"github.com/vasilisp/wikai/internal/util"
	"github.com/vasilisp/wikai/pkg/api"
	"github.com/vasilisp/wikai/pkg/search"
)

type WikiRW interface {
	Read(path string) (string, error)
	Write(path string, content string, embedding []float64) error
}

const recentChatsLimit = 10

type recentChats struct {
	mu    sync.Mutex
	cache *lru.Cache
}

func (s *recentChats) add(key string, value lingograph.Chat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache.Add(key, value)
}

func sanitizeKey(key string) string {
	var out []rune
	for _, r := range key {
		if unicode.IsPrint(r) {
			out = append(out, r)
		}
	}
	return string(out)
}

func (s *recentChats) get(key string) (value lingograph.Chat, ok bool) {
	if key == "" {
		return nil, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if val, ok := s.cache.Get(key); ok {
		log.Printf("continuing chat %s", sanitizeKey(key))
		return val.(lingograph.Chat), true
	}

	log.Printf("key not found: %s", sanitizeKey(key))
	return nil, false
}

// Ctx represents the context of the backai package
type Ctx interface {
	// Embed converts a string into a vector of float64 values
	Embed(content string) ([]float64, error)
	// Query sends a query to the backend LLM, possibly using the chat history
	// represented by the chatId
	Query(userQuery string, chatId string) (api.PostResponse, error)
	// DB provides access to the underlying database handle
	DB() search.DB
	seal()
}

type ctx struct {
	pipelineSearch    lingograph.Pipeline
	pipelineSummarize lingograph.Pipeline
	doSummarizeVar    store.Var[bool]
	responseVar       store.Var[api.PostResponse]
	wikiPrefix        string
	embeddingClient   EmbeddingClient
	db                search.DB
	recentChats       recentChats
}

func (ctx *ctx) seal() {}

func (ctx *ctx) DB() search.DB {
	return ctx.db
}

func (ctx *ctx) Embed(content string) ([]float64, error) {
	util.Assert(ctx != nil, "Ctx is nil")
	return ctx.embeddingClient.Embed(content)
}

type WriteArgs struct {
	Path    string `json:"path" jsonschema:"title=Note Path,description=The note path, suitable for a web URL; must be lowercase letters (a-z), digits (0-9), or hyphens (-) only, with no slashes or subdirectories.,pattern=^[a-z0-9-]+$,examples=[\"daily-notes\", \"meeting-20250424\"]"`
	Content string `json:"content" jsonschema:"title=Note Content,description=Markdown-formatted content to write"`
}

type SearchArgs struct {
	Query string
}

func doSearch(embeddingClient EmbeddingClient, db search.DB, query string) ([]string, error) {
	vector, err := embeddingClient.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("failed to vectorize query: %v", err)
	}

	results, err := db.Search(vector, 5)
	if err != nil {
		return nil, fmt.Errorf("search failed: %v", err)
	}

	paths := make([]string, len(results))
	for i, result := range results {
		paths[i] = result.Path
	}

	return paths, nil
}

func pipelineSearch(client openai.Client, db search.DB, embeddingClient EmbeddingClient, wiki WikiRW, wikiPrefix string, doSummarizeVar store.Var[bool], responseVar store.Var[api.PostResponse]) lingograph.Pipeline {
	actor := openai.NewActor(client, openai.GPT41Mini, data.SystemPrompt, nil)

	openai.AddFunction(actor, "write", "Write a new note", func(args WriteArgs, r store.Store) (api.PostResponse, error) {
		embedding, err := embeddingClient.Embed(args.Content)
		if err != nil {
			return api.PostResponse{}, fmt.Errorf("failed to embed content: %v", err)
		}

		err = wiki.Write(args.Path, args.Content, embedding)

		db.Add(args.Path, embedding, time.Now())

		if err != nil {
			return api.PostResponse{}, err
		}

		response := api.PostResponse{
			Message:         fmt.Sprintf("I saved a new note for you: %s", args.Path),
			References:      []string{args.Path},
			ReferencePrefix: wikiPrefix,
		}

		store.Set(r, responseVar, response)

		return response, nil
	})

	openai.AddFunctionUnsafe(actor, "search", "Search for notes", func(query SearchArgs, r store.Store) ([]string, error) {
		log.Printf("search query: %s", query.Query)

		pages, err := doSearch(embeddingClient, db, query.Query)
		if err != nil {
			return nil, err
		}

		if len(pages) == 0 {
			return []string{"nothing relevant found"}, nil
		}

		store.Set(r, doSummarizeVar, true)

		log.Printf("search results: %v", pages)

		response := make([]string, 0, len(pages))
		for _, page := range pages {
			content, err := wiki.Read(page)
			if err != nil {
				return nil, err
			}

			response = append(response, fmt.Sprintf("relevant document %s\n---\n%s", page, content))
		}

		return response, nil
	})

	return actor.Pipeline(nil, false, 3)
}

type Summary struct {
	Text       string   `json:"text" jsonschema:"description:Summary text"`
	Relevant   []string `json:"relevant" jsonschema:"description:List of opaque document IDs that are relevant (do not summarize or rephrase)"`
	Irrelevant []string `json:"irrelevant" jsonschema:"description:List of opaque document IDs that are irrelevant (do not summarize or rephrase)"`
}

func pipelineSummarize(client openai.Client, wikiPrefix string, responseVar store.Var[api.PostResponse]) lingograph.Pipeline {
	actor := openai.NewActor(client, openai.GPT41Mini, data.SystemPromptSummarize, nil)

	openai.AddFunction(actor, "summarize", "Summarize notes", func(summary Summary, r store.Store) (api.PostResponse, error) {
		response := api.PostResponse{
			Message:         summary.Text,
			References:      summary.Relevant,
			ReferencePrefix: wikiPrefix,
		}

		store.Set(r, responseVar, response)
		return response, nil
	})

	return actor.Pipeline(nil, false, 3)
}

func NewCtx(wiki WikiRW, wikiPrefix string, apiKey string, embeddingDimensions int) Ctx {
	client := openai.NewClient(apiKey)

	doSummarizeVar := store.FreshVar[bool]()
	responseVar := store.FreshVar[api.PostResponse]()

	embeddingClient := NewEmbeddingClient(apiKey, embeddingDimensions)
	db := search.NewDB()

	return &ctx{
		pipelineSearch:    pipelineSearch(client, db, embeddingClient, wiki, wikiPrefix, doSummarizeVar, responseVar),
		pipelineSummarize: pipelineSummarize(client, wikiPrefix, responseVar),
		responseVar:       responseVar,
		doSummarizeVar:    doSummarizeVar,
		wikiPrefix:        wikiPrefix,
		embeddingClient:   embeddingClient,
		db:                db,
		recentChats:       recentChats{cache: lru.New(recentChatsLimit)},
	}
}

func (ctx *ctx) Query(userQuery string, chatId string) (api.PostResponse, error) {
	chat, ok := ctx.recentChats.get(chatId)
	if !ok {
		chatId = uuid.New().String()
		log.Printf("new chat %s", chatId)
		chat = lingograph.NewChat()
		ctx.recentChats.add(chatId, chat)
	}

	pipeline := lingograph.Chain(
		lingograph.UserPrompt(userQuery, false),
		ctx.pipelineSearch,
		lingograph.If(
			func(r store.StoreRO) bool {
				doSummarize, ok := store.GetRO(r, ctx.doSummarizeVar)
				return ok && doSummarize
			},
			ctx.pipelineSummarize,
			lingograph.Chain(),
		),
	)

	err := pipeline.Execute(chat)
	if err != nil {
		return api.PostResponse{}, err
	}

	history := chat.History()

	if history.Len() == 0 {
		return api.PostResponse{}, errors.New("no messages")
	}

	responseVal, ok := lingograph.Get(chat, ctx.responseVar)
	if ok {
		return responseVal, nil
	}

	doSummarize, ok := lingograph.Get(chat, ctx.doSummarizeVar)
	if ok && doSummarize {
		return api.PostResponse{}, errors.New("internal error: no response")
	}

	return api.PostResponse{
		Message: history.At(history.Len() - 1).Content,
		ChatID:  chatId,
	}, nil
}
