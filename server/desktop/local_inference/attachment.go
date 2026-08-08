//go:build desktop

package local_inference

// Attachment is a protocol-agnostic attachment payload that the Engine feeds to
// the local model. The openai/anthropic adapters translate it into each
// protocol's multimodal content shape. Defined here (not in local_render) so
// the Engine consumes its own type without importing the storage package.
type Attachment struct {
	Kind     string `json:"kind"` // "image" | "text"
	MimeType string `json:"mime_type"`
	// Base64 holds raw image bytes (base64) when Kind == "image".
	Base64 string `json:"-"` // never echoed in JSON responses
	// Text holds extracted document/text content when Kind == "text".
	Text string `json:"-"`
}

// AttachmentLoader resolves file ids to model-ready attachments. Implemented by
// desktop (local_render.Store: looks up w_workagent_thread_file + ExtractFile)
// and injected into the Engine, so the Engine stays free of DB/filesystem code.
// uid scopes the lookup so a stale cross-account file id cannot leak content.
type AttachmentLoader interface {
	Load(fileIDs []int64, uid uint64) ([]Attachment, error)
}
