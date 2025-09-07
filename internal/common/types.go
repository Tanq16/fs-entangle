package common

import "encoding/json"

type MessageType string

const (
	// Server -> Client
	TypeManifest           MessageType = "manifest"
	TypeFileContent        MessageType = "file_content"
	TypeUpdateNotification MessageType = "update_notification"

	// Client -> Server
	TypeFileRequest MessageType = "file_request"
)

const (
	OpCreate = "create"
	OpWrite  = "write"
	OpRemove = "remove"
)

type MessageWrapper struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type ManifestMessage struct {
	Files map[string]string `json:"files"` // Path -> Hash
}

type UpdateNotificationMessage struct {
	Op   string `json:"op"`
	Path string `json:"path"`
	Hash string `json:"hash,omitempty"`
}

type FileRequestMessage struct {
	Paths []string `json:"paths"`
}

type FileContentMessage struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}
