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
	id   string
	conn *websocket.Conn
}

type Server struct {
	cfg       Config
	upgrader  websocket.Upgrader
	clients   sync.Map // map[string]*clientConnection
	diskMutex sync.Mutex
	ignorer   *common.PathIgnorer
}

func New(cfg Config) (*Server, error) {
	if err := os.MkdirAll(cfg.SyncDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create sync directory: %w", err)
	}
	return &Server{
		cfg: cfg,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		ignorer: common.NewPathIgnorer(cfg.IgnorePaths),
	}, nil
}

func (s *Server) Run() error {
	http.HandleFunc("/ws", s.handleConnections)
	addr := fmt.Sprintf(":%d", s.cfg.Port)
	log.Info().Msgf("WebSocket server listening on %s", addr)
	return http.ListenAndServe(addr, nil)
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := s.upgrader.Upgrade(w, r, nil)
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
	manifest, err := common.BuildFileManifest(s.cfg.SyncDir, s.ignorer)
	if err != nil {
		return fmt.Errorf("could not build file manifest: %w", err)
	}
	payload, _ := json.Marshal(common.ManifestMessage{Files: manifest})
	msg := common.MessageWrapper{
		Type:    common.TypeManifest,
		Payload: payload,
	}
	s.diskMutex.Lock()
	defer s.diskMutex.Unlock()
	return client.conn.WriteJSON(msg)
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
		case common.TypeUpdateNotification:
			s.handleUpdateNotification(client, wrapper.Payload)
		}
	}
}

func (s *Server) handleFileRequest(client *clientConnection, payload []byte) {
	var req common.FileRequestMessage
	if err := json.Unmarshal(payload, &req); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal file request")
		return
	}
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
		s.diskMutex.Lock()
		err = client.conn.WriteJSON(msg)
		s.diskMutex.Unlock()
		if err != nil {
			log.Error().Err(err).Str("client_id", client.id).Msg("Failed to send file content")
		}
	}
}

func (s *Server) handleUpdateNotification(sender *clientConnection, payload []byte) {
	var update common.UpdateNotificationMessage
	if err := json.Unmarshal(payload, &update); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal update notification")
		return
	}
	if s.ignorer.IsIgnored(update.Path) {
		log.Debug().Str("path", update.Path).Msg("Ignoring update notification based on server rules")
		return
	}
	log.Info().Str("op", update.Op).Str("path", update.Path).Str("client_id", sender.id).Msg("Received update from client")

	// Server does not automatically apply change; it expects to receive file content next.
	// It just relays the notification. A more robust implementation might verify the change first.

	s.broadcastUpdate(sender.id, update)
}

func (s *Server) applyChangeLocally(update common.UpdateNotificationMessage, content []byte) {
	s.diskMutex.Lock()
	defer s.diskMutex.Unlock()
	fullPath := filepath.Join(s.cfg.SyncDir, update.Path)
	switch update.Op {
	case common.OpCreate, common.OpWrite:
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			log.Error().Err(err).Str("path", fullPath).Msg("Failed to create parent directories")
			return
		}
		if err := os.WriteFile(fullPath, content, 0644); err != nil {
			log.Error().Err(err).Str("path", fullPath).Msg("Failed to write file")
		}
	case common.OpRemove:
		if err := os.Remove(fullPath); err != nil {
			log.Error().Err(err).Str("path", fullPath).Msg("Failed to remove file")
		}
	}
}

func (s *Server) broadcastUpdate(senderID string, update common.UpdateNotificationMessage) {
	payload, _ := json.Marshal(update)
	msg := common.MessageWrapper{
		Type:    common.TypeUpdateNotification,
		Payload: payload,
	}
	s.clients.Range(func(key, value interface{}) bool {
		id := key.(string)
		client := value.(*clientConnection)
		if id != senderID {
			s.diskMutex.Lock()
			err := client.conn.WriteJSON(msg)
			s.diskMutex.Unlock()
			if err != nil {
				log.Error().Err(err).Str("client_id", id).Msg("Failed to broadcast update")
			}
		}
		return true
	})
}
