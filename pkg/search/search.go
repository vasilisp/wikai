package search

import (
	"container/heap"
	"fmt"
	"sort"
	"time"

	"github.com/vasilisp/wikai/internal/util"
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

type row struct {
	vector []float64
	stamp  time.Time
}

type DB struct {
	rows map[string]row
}

func NewDB() DB {
	rows := make(map[string]row)

	return DB{rows: rows}
}

func (db DB) Add(id string, emb []float64, stamp time.Time) {
	util.Assert(db.rows != nil, "Add nil embeddings")

	if _, ok := db.rows[id]; ok {
		if db.rows[id].stamp.After(stamp) {
			return
		}
	}

	db.rows[id] = row{vector: emb, stamp: stamp}
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
	util.Assert(db.rows != nil, "Search nil embeddings")

	bestResults := NewBestResults(maxResults)

	// brute-force, calculate cosine similarity with all embeddings
	for id, row := range db.rows {
		if row.vector == nil {
			continue
		}

		distance := cosineDistance(query, row.vector)

		bestResults.Add(Result{
			Path:     id,
			Distance: distance,
		})
	}

	return bestResults.Get(), nil
}

func (db DB) Stats() string {
	util.Assert(db.rows != nil, "Stats nil embeddings")

	return fmt.Sprintf("DB stats: %d rows", len(db.rows))
}
