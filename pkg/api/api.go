package api

const PostPath = "/ai"
const IndexPath = "/index"

type Page struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Path    string `json:"path"`
	Stamp   int64  `json:"stamp"`
}

type HistoryEntry struct {
	Text   string `json:"text"`
	IsUser bool   `json:"is_user"`
}

type PostRequest struct {
	Message string `json:"message"`
	ChatID  string `json:"chat_id"`
}
type PostResponse struct {
	Message         string   `json:"message,omitempty" jsonschema:"description:human-readable response message without any formatting"`
	References      []string `json:"references,omitempty" jsonschema:"description:IDs of relevant documents; NOT the whole content of each document"`
	ReferencePrefix string   `json:"reference_prefix,omitempty" jsonschema:"description:Web path for the reference IDs"`
	ChatID          string   `json:"chat_id"`
}
