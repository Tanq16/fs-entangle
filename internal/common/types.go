package common

import "encoding/json"

type MessageType string

const (
	// Server to Client on connection - list of files and their hashes
	TypeManifest MessageType = "manifest"

	// Client to Server during initial sync - content for a list of files
	TypeFileRequest MessageType = "file_request"

	// Server to Client during initial sync - content of a single requested file
	TypeFileContent MessageType = "file_content"

	// Bidirectional for runtime changes
	// Client -> Server - Informs about local change
	// Server -> Client - Broadcasts change to other clients
	TypeFileOperation MessageType = "file_operation"
)

type OperationType string

const (
	OpWrite  OperationType = "write"
	OpRemove OperationType = "remove"
)

type MessageWrapper struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type ManifestMessage struct {
	Files map[string]string `json:"files"`
}

type FileRequestMessage struct {
	Paths []string `json:"paths"`
}

type FileContentMessage struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

type FileOperationMessage struct {
	Op      OperationType `json:"op"`
	Path    string        `json:"path"`
	Content []byte        `json:"content"`
	IsDir   bool          `json:"is_dir,omitempty"`
}
