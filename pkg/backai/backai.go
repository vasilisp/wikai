// general AI backend functionality

package backai

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/vasilisp/lingograph"
	"github.com/vasilisp/lingograph/openai"
	"github.com/vasilisp/lingograph/store"
	"github.com/vasilisp/wikai/internal/data"
	"github.com/vasilisp/wikai/internal/util"
	"github.com/vasilisp/wikai/pkg/search"
)

type WikiRW interface {
	Read(path string) (string, error)
	Write(path string, content string, embedding []float64) error
}

type Ctx struct {
	pipelineSearch    lingograph.Pipeline
	pipelineSummarize lingograph.Pipeline
	doSummarizeVar    store.Var[bool]
	responseVar       store.Var[Response]
	wikiPrefix        string
	embeddingClient   EmbeddingClient
	db                search.DB
}

func (ctx *Ctx) DB() search.DB {
	return ctx.db
}

func (ctx *Ctx) Embed(content string) ([]float64, error) {
	util.Assert(ctx != nil, "Ctx is nil")
	return ctx.embeddingClient.Embed(content)
}

type WriteArgs struct {
	Path    string `json:"path" jsonschema:"description:path for the note to write, suitable for a web URL; only lowercase characters a-z, 0-9, and - are allowed"`
	Content string `json:"content" jsonschema:"description:Markdown-formattedcontent to write"`
}

type SearchArgs struct {
	Query string
}

type Response struct {
	Message         string   `json:"message,omitempty" jsonschema:"description:human-readable response message without any formatting"`
	References      []string `json:"references,omitempty" jsonschema:"description:IDs of relevant documents; NOT the whole content of each document"`
	ReferencePrefix string   `json:"reference_prefix,omitempty" jsonschema:"description:Web path for the reference IDs"`
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

func pipelineSearch(model openai.Model, db search.DB, embeddingClient EmbeddingClient, wiki WikiRW, wikiPrefix string, doSummarizeVar store.Var[bool], responseVar store.Var[Response]) lingograph.Pipeline {
	actor := openai.NewActor(model, data.SystemPrompt)

	openai.AddFunction(actor, "write", "Write a new note", func(args WriteArgs, r store.Store) (Response, error) {
		embedding, err := embeddingClient.Embed(args.Content)
		if err != nil {
			return Response{}, fmt.Errorf("failed to embed content: %v", err)
		}

		err = wiki.Write(args.Path, args.Content, embedding)

		db.Add(args.Path, embedding, time.Now())

		if err != nil {
			return Response{}, err
		}

		response := Response{
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
	Text       string   `json:"text" jsonschema:"description:summary text"`
	Relevant   []string `json:"relevant" jsonschema:"description:IDs of relevant documents"`
	Irrelevant []string `json:"irrelevant" jsonschema:"description:IDs of irrelevant documents"`
}

func pipelineSummarize(model openai.Model, wikiPrefix string, responseVar store.Var[Response]) lingograph.Pipeline {
	actor := openai.NewActor(model, data.SystemPromptSummarize)

	openai.AddFunction(actor, "summarize", "Summarize notes", func(summary Summary, r store.Store) (Response, error) {
		response := Response{
			Message:         summary.Text,
			References:      summary.Relevant,
			ReferencePrefix: wikiPrefix,
		}

		store.Set(r, responseVar, response)
		return response, nil
	})

	return actor.Pipeline(nil, false, 3)
}

func NewCtx(wiki WikiRW, wikiPrefix string, apiKey string, embeddingDimensions int) *Ctx {
	model := openai.NewModel(openai.GPT4o, apiKey)

	doSummarizeVar := store.FreshVar[bool]()
	responseVar := store.FreshVar[Response]()

	embeddingClient := NewEmbeddingClient(apiKey, embeddingDimensions)
	db := search.NewDB()

	return &Ctx{
		pipelineSearch:    pipelineSearch(model, db, embeddingClient, wiki, wikiPrefix, doSummarizeVar, responseVar),
		pipelineSummarize: pipelineSummarize(model, wikiPrefix, responseVar),
		responseVar:       responseVar,
		doSummarizeVar:    doSummarizeVar,
		wikiPrefix:        wikiPrefix,
		embeddingClient:   embeddingClient,
		db:                db,
	}
}

func (ctx *Ctx) Query(userQuery string) (Response, error) {
	chat := lingograph.NewSliceChat()

	pipeline := lingograph.Chain(
		lingograph.UserPrompt(userQuery, false),
		ctx.pipelineSearch,
		lingograph.If(ctx.doSummarizeVar,
			ctx.pipelineSummarize,
			lingograph.Chain(),
		),
	)

	err := pipeline.Execute(chat)
	if err != nil {
		return Response{}, err
	}

	if len(chat.History()) == 0 {
		return Response{}, errors.New("no messages")
	}

	responseVal, ok := store.Get(chat.Store(), ctx.responseVar)
	if ok {
		return responseVal, nil
	}

	doSummarize, ok := store.Get(chat.Store(), ctx.doSummarizeVar)
	if ok && doSummarize {
		return Response{}, errors.New("internal error: no response")
	}

	return Response{
		Message: chat.History()[len(chat.History())-1].Content,
	}, nil
}
