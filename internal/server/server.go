package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"github.com/tanq16/fs-entangle/internal/common"
)

type Config struct {
	Port        int
	SyncDir     string
	IgnorePaths string
}

type clientConnection struct {
	id         string
	conn       *websocket.Conn
	writeMutex sync.Mutex
}

type fileOperationEnvelope struct {
	senderID string
	op       common.FileOperationMessage
}

type Server struct {
	cfg       Config
	clients   sync.Map // A concurrent map to store clients: map[string]*clientConnection
	ignorer   *common.PathIgnorer
	opChan    chan fileOperationEnvelope
	diskMutex sync.Mutex
}

func New(cfg Config) (*Server, error) {
	if err := os.MkdirAll(cfg.SyncDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create sync directory: %w", err)
	}
	return &Server{
		cfg:     cfg,
		ignorer: common.NewPathIgnorer(cfg.IgnorePaths),
		// Buffered channel to act as the operation ingest queue
		opChan: make(chan fileOperationEnvelope, 100),
	}, nil
}

func (s *Server) Run() error {
	// Central goroutine to process all incoming operations serially
	go s.processOperationQueue()
	http.HandleFunc("/ws", s.handleConnections)
	addr := fmt.Sprintf(":%d", s.cfg.Port)
	log.Info().Str("address", addr).Msg("WebSocket server starting to listen")
	return http.ListenAndServe(addr, nil)
}

func (s *Server) processOperationQueue() {
	log.Info().Msg("Starting file operation queue processor")
	for envelope := range s.opChan {
		log.Info().Str("op", string(envelope.op.Op)).Str("path", envelope.op.Path).Str("client_id", envelope.senderID).Msg("Processing operation from queue")
		s.applyChangeLocally(&envelope.op)
		s.broadcastOperation(envelope.senderID, &envelope.op)
	}
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("Failed to upgrade connection")
		return
	}
	defer ws.Close()
	client := &clientConnection{
		id:   uuid.NewString(),
		conn: ws,
	}
	s.clients.Store(client.id, client)
	log.Info().Str("client_id", client.id).Str("addr", ws.RemoteAddr().String()).Msg("Client connected")
	defer func() {
		s.clients.Delete(client.id)
		log.Info().Str("client_id", client.id).Msg("Client disconnected")
	}()
	if err := s.sendInitialManifest(client); err != nil {
		log.Error().Err(err).Str("client_id", client.id).Msg("Failed to send initial manifest")
		return
	}
	s.handleClientMessages(client)
}

func (s *Server) sendInitialManifest(client *clientConnection) error {
	log.Info().Str("client_id", client.id).Msg("Building and sending initial manifest")
	manifest, err := common.BuildFileManifest(s.cfg.SyncDir, s.ignorer)
	if err != nil {
		return fmt.Errorf("could not build file manifest: %w", err)
	}
	payload, _ := json.Marshal(common.ManifestMessage{Files: manifest})
	msg := common.MessageWrapper{
		Type:    common.TypeManifest,
		Payload: payload,
	}
	return s.sendMessage(client, msg)
}

func (s *Server) handleClientMessages(client *clientConnection) {
	for {
		var wrapper common.MessageWrapper
		if err := client.conn.ReadJSON(&wrapper); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Error().Err(err).Str("client_id", client.id).Msg("Client read error")
			}
			break
		}
		switch wrapper.Type {
		case common.TypeFileRequest:
			s.handleFileRequest(client, wrapper.Payload)
		case common.TypeFileOperation:
			s.handleFileOperation(client, wrapper.Payload)
		default:
			log.Warn().Str("type", string(wrapper.Type)).Msg("Received unknown message type from client")
		}
	}
}

func (s *Server) handleFileRequest(client *clientConnection, payload []byte) {
	var req common.FileRequestMessage
	if err := json.Unmarshal(payload, &req); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal file request")
		return
	}
	log.Info().Int("count", len(req.Paths)).Str("client_id", client.id).Msg("Handling file request")
	for _, path := range req.Paths {
		if s.ignorer.IsIgnored(path) {
			continue
		}
		fullPath := filepath.Join(s.cfg.SyncDir, path)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			log.Error().Err(err).Str("path", path).Msg("Failed to read file for client request")
			continue
		}
		contentPayload, _ := json.Marshal(common.FileContentMessage{Path: path, Content: content})
		msg := common.MessageWrapper{
			Type:    common.TypeFileContent,
			Payload: contentPayload,
		}
		if err := s.sendMessage(client, msg); err != nil {
			log.Error().Err(err).Str("client_id", client.id).Msg("Failed to send file content")
			break
		}
	}
}

func (s *Server) handleFileOperation(sender *clientConnection, payload []byte) {
	var op common.FileOperationMessage
	if err := json.Unmarshal(payload, &op); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal file operation")
		return
	}
	if s.ignorer.IsIgnored(op.Path) {
		log.Debug().Str("path", op.Path).Msg("Ignoring file operation based on server rules")
		return
	}
	log.Debug().Str("path", op.Path).Str("client_id", sender.id).Msg("Received and queuing file operation")
	s.opChan <- fileOperationEnvelope{
		senderID: sender.id,
		op:       op,
	}
}

func (s *Server) applyChangeLocally(op *common.FileOperationMessage) {
	s.diskMutex.Lock()
	defer s.diskMutex.Unlock()
	fullPath := filepath.Join(s.cfg.SyncDir, op.Path)
	switch op.Op {
	case common.OpWrite:
		if op.IsDir {
			if err := os.MkdirAll(fullPath, 0755); err != nil {
				log.Error().Err(err).Str("path", fullPath).Msg("Failed to create directory")
			}
			return
		}
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			log.Error().Err(err).Str("path", fullPath).Msg("Failed to create parent directories")
			return
		}
		if err := os.WriteFile(fullPath, op.Content, 0644); err != nil {
			log.Error().Err(err).Str("path", fullPath).Msg("Failed to write file")
		}
	case common.OpRemove:
		if err := os.RemoveAll(fullPath); err != nil {
			log.Error().Err(err).Str("path", fullPath).Msg("Failed to remove file/directory")
		}
	}
}

func (s *Server) broadcastOperation(senderID string, op *common.FileOperationMessage) {
	payload, _ := json.Marshal(op)
	msg := common.MessageWrapper{
		Type:    common.TypeFileOperation,
		Payload: payload,
	}
	s.clients.Range(func(key, value interface{}) bool {
		id := key.(string)
		client := value.(*clientConnection)
		if id != senderID {
			if err := s.sendMessage(client, msg); err != nil {
				log.Error().Err(err).Str("client_id", id).Msg("Failed to broadcast operation")
			}
		}
		return true // continue iteration
	})
}

func (s *Server) sendMessage(client *clientConnection, message common.MessageWrapper) error {
	client.writeMutex.Lock()
	defer client.writeMutex.Unlock()
	return client.conn.WriteJSON(message)
}
