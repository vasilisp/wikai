package api

const PostPath = "/ai"

type Page struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Path    string `json:"path"`
	Stamp   int64  `json:"stamp"`
}

type PostResponse struct {
	Response string `json:"response"`
}
