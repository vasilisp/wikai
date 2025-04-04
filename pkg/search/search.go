package search

import (
	"container/heap"
	"fmt"
	"sort"

	"github.com/vasilisp/wikai/pkg/embedding"
	"gonum.org/v1/gonum/mat"
)

type Result struct {
	Path     string
	Distance float64
}

// Compute cosine similarity
func cosineSimilarity(a, b []float64) float64 {
	va := mat.NewVecDense(len(a), a)
	vb := mat.NewVecDense(len(b), b)
	dotProduct := mat.Dot(va, vb)
	normA := mat.Norm(va, 2)
	normB := mat.Norm(vb, 2)
	return dotProduct / (normA * normB)
}

func cosineDistance(a, b []float64) float64 {
	return 1 - cosineSimilarity(a, b)
}

type DB struct {
	embeddings *[]embedding.Embedding
}

func NewDB() DB {
	embeddings := make([]embedding.Embedding, 0, 128)

	return DB{embeddings: &embeddings}
}

func (db DB) Add(id string, emb []float64) {
	*db.embeddings = append(*db.embeddings, embedding.Embedding{ID: id, Vector: emb})
}

type resultHeap []Result

func (h resultHeap) Len() int           { return len(h) }
func (h resultHeap) Less(i, j int) bool { return h[i].Distance > h[j].Distance }
func (h resultHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *resultHeap) Push(x interface{}) {
	*h = append(*h, x.(Result))
}

func (h *resultHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

type bestResults struct {
	results *resultHeap
	maxSize int
}

func NewBestResults(maxSize int) bestResults {
	results := resultHeap{}
	heap.Init(&results)

	return bestResults{
		results: &results,
		maxSize: maxSize,
	}
}

func (br bestResults) Add(result Result) {
	heap.Push(br.results, result)
	if br.results.Len() > br.maxSize {
		heap.Pop(br.results)
	}
}

func (br bestResults) Get() []Result {
	results := *br.results
	sort.Slice(results, func(i, j int) bool {
		return results[i].Distance < results[j].Distance
	})
	return results
}

func (db DB) Search(query []float64, maxResults int) ([]Result, error) {
	if db.embeddings == nil {
		return nil, fmt.Errorf("Search nil embeddings")
	}

	bestResults := NewBestResults(maxResults)

	// brute-force, calculate cosine similarity with all embeddings
	for _, emb := range *db.embeddings {
		if emb.Vector == nil {
			continue
		}

		distance := cosineDistance(query, emb.Vector)

		bestResults.Add(Result{
			Path:     emb.ID,
			Distance: distance,
		})
	}

	return bestResults.Get(), nil
}

func (db DB) Stats() string {
	return fmt.Sprintf("DB stats: %d embeddings", len(*db.embeddings))
}
