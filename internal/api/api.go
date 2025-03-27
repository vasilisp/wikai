package api

const PostPath = "/ai"

type Page struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Path    string `json:"path"`
	Stamp   int64  `json:"stamp"`
}

type PostResponse struct {
	Message         string   `json:"message"`
	ReferencePrefix string   `json:"reference_prefix,omitempty"`
	References      []string `json:"references,omitempty"`
}
