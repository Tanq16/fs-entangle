package client

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"github.com/tanq16/fs-entangle/internal/common"
)

type Config struct {
	ServerAddr  string
	SyncDir     string
	IgnorePaths string
}

type Client struct {
	cfg         Config
	conn        *websocket.Conn
	watcher     *fsnotify.Watcher
	ignorer     *common.PathIgnorer
	isSyncing   bool
	writeEvents map[string]time.Time
}

func New(cfg Config) (*Client, error) {
	if err := os.MkdirAll(cfg.SyncDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create sync directory: %w", err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create file watcher: %w", err)
	}
	return &Client{
		cfg:         cfg,
		watcher:     watcher,
		ignorer:     common.NewPathIgnorer(cfg.IgnorePaths),
		writeEvents: make(map[string]time.Time),
	}, nil
}

func (c *Client) Run() {
	defer c.watcher.Close()
	go c.watchFilesystem()
	for {
		c.connect()
		c.listenToServer()
		log.Warn().Msg("Disconnected from server. Retrying in 5 seconds...")
		time.Sleep(5 * time.Second)
	}
}

func (c *Client) connect() {
	u, err := url.Parse(c.cfg.ServerAddr)
	if err != nil {
		log.Fatal().Err(err).Msg("Invalid server URL")
	}
	for {
		conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to connect to server. Retrying...")
			time.Sleep(5 * time.Second)
			continue
		}
		c.conn = conn
		log.Info().Str("addr", c.cfg.ServerAddr).Msg("Connected to server")
		return
	}
}

func (c *Client) listenToServer() {
	defer c.conn.Close()
	for {
		var wrapper common.MessageWrapper
		if err := c.conn.ReadJSON(&wrapper); err != nil {
			log.Error().Err(err).Msg("Error reading from server")
			return
		}
		c.isSyncing = true
		switch wrapper.Type {
		case common.TypeManifest:
			c.handleManifest(wrapper.Payload)
		case common.TypeFileContent:
			c.handleFileContent(wrapper.Payload)
		case common.TypeUpdateNotification:
			c.handleUpdateNotification(wrapper.Payload)
		}
		c.isSyncing = false
	}
}

func (c *Client) handleManifest(payload []byte) {
	var msg common.ManifestMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal manifest")
		return
	}
	log.Info().Msg("Received server manifest. Starting initial sync.")

	localManifest, err := common.BuildFileManifest(c.cfg.SyncDir, c.ignorer)
	if err != nil {
		log.Error().Err(err).Msg("Failed to build local manifest for sync")
		return
	}
	var toRequest []string
	serverFiles := make(map[string]bool)

	for path, serverHash := range msg.Files {
		serverFiles[path] = true
		localHash, exists := localManifest[path]
		if !exists || localHash != serverHash {
			toRequest = append(toRequest, path)
		}
	}
	for path := range localManifest {
		if !serverFiles[path] {
			fullPath := filepath.Join(c.cfg.SyncDir, path)
			log.Info().Str("path", path).Msg("Removing local file not present on server")
			os.RemoveAll(fullPath)
		}
	}

	if len(toRequest) > 0 {
		log.Info().Int("count", len(toRequest)).Msg("Requesting files from server")
		c.requestFiles(toRequest)
	} else {
		log.Info().Msg("Initial sync complete. Local directory is up-to-date.")
	}
}

func (c *Client) handleFileContent(payload []byte) {
	var msg common.FileContentMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal file content")
		return
	}
	log.Info().Str("path", msg.Path).Msg("Received file content from server")
	fullPath := filepath.Join(c.cfg.SyncDir, msg.Path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		log.Error().Err(err).Msg("Failed to create parent directories")
		return
	}
	if err := os.WriteFile(fullPath, msg.Content, 0644); err != nil {
		log.Error().Err(err).Str("path", msg.Path).Msg("Failed to write file")
	}
}

func (c *Client) handleUpdateNotification(payload []byte) {
	var msg common.UpdateNotificationMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal update notification")
		return
	}
	log.Info().Str("op", msg.Op).Str("path", msg.Path).Msg("Received update notification")
	fullPath := filepath.Join(c.cfg.SyncDir, msg.Path)
	switch msg.Op {
	case common.OpRemove:
		os.RemoveAll(fullPath)
	case common.OpCreate, common.OpWrite:
		localHash, err := common.ComputeFileHash(fullPath)
		if err != nil || localHash != msg.Hash {
			c.requestFiles([]string{msg.Path})
		}
	}
}

func (c *Client) requestFiles(paths []string) {
	payload, _ := json.Marshal(common.FileRequestMessage{Paths: paths})
	msg := common.MessageWrapper{
		Type:    common.TypeFileRequest,
		Payload: payload,
	}
	if err := c.conn.WriteJSON(msg); err != nil {
		log.Error().Err(err).Msg("Failed to send file request to server")
	}
}

func (c *Client) sendUpdateNotification(op, path, hash string) {
	relPath, err := filepath.Rel(c.cfg.SyncDir, path)
	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("Failed to get relative path")
		return
	}
	payload, _ := json.Marshal(common.UpdateNotificationMessage{Op: op, Path: relPath, Hash: hash})
	msg := common.MessageWrapper{
		Type:    common.TypeUpdateNotification,
		Payload: payload,
	}
	if c.conn != nil {
		if err := c.conn.WriteJSON(msg); err != nil {
			log.Error().Err(err).Msg("Failed to send update notification to server")
		}
	}
}

func (c *Client) watchFilesystem() {
	// Initial recursive watch setup
	filepath.Walk(c.cfg.SyncDir, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			relPath, _ := filepath.Rel(c.cfg.SyncDir, path)
			if !c.ignorer.IsIgnored(relPath) {
				c.watcher.Add(path)
			}
		}
		return nil
	})

	for {
		select {
		case event, ok := <-c.watcher.Events:
			if !ok {
				return
			}
			if c.isSyncing {
				continue // Ignore events generated by our own sync process
			}
			relPath, err := filepath.Rel(c.cfg.SyncDir, event.Name)
			if err != nil || c.ignorer.IsIgnored(relPath) {
				continue
			}
			// Debounce write events
			if event.Op&fsnotify.Write == fsnotify.Write {
				if time.Since(c.writeEvents[event.Name]) < 1*time.Second {
					continue
				}
				c.writeEvents[event.Name] = time.Now()
			}
			c.handleFsEvent(event)
		case err, ok := <-c.watcher.Errors:
			if !ok {
				return
			}
			log.Error().Err(err).Msg("Watcher error")
		}
	}
}

func (c *Client) handleFsEvent(event fsnotify.Event) {
	var op, hash string
	var err error
	info, statErr := os.Stat(event.Name)
	if event.Op&fsnotify.Create == fsnotify.Create {
		op = common.OpCreate
		if info.IsDir() {
			c.watcher.Add(event.Name) // Watch new directories
		} else {
			hash, err = common.ComputeFileHash(event.Name)
		}
	} else if event.Op&fsnotify.Write == fsnotify.Write {
		op = common.OpWrite
		hash, err = common.ComputeFileHash(event.Name)
	} else if event.Op&fsnotify.Remove == fsnotify.Remove || event.Op&fsnotify.Rename == fsnotify.Rename {
		op = common.OpRemove
		c.watcher.Remove(event.Name)
	}
	if err != nil {
		log.Error().Err(err).Str("path", event.Name).Msg("Error processing file for notification")
		return
	}
	if op != "" && (op == common.OpRemove || (op != common.OpRemove && statErr == nil && !info.IsDir())) {
		log.Info().Str("op", op).Str("path", event.Name).Msg("Detected local file change")
		c.sendUpdateNotification(op, event.Name, hash)
	}
}
