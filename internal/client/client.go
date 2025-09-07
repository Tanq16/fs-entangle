package client

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
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
	cfg     Config
	conn    *websocket.Conn
	watcher *fsnotify.Watcher
	ignorer *common.PathIgnorer
	// isSyncing prevents the watcher from reacting to changes made by the client itself
	syncingMutex sync.Mutex
	isSyncing    bool
	writeMutex   sync.Mutex
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
		cfg:     cfg,
		watcher: watcher,
		ignorer: common.NewPathIgnorer(cfg.IgnorePaths),
	}, nil
}

func (c *Client) Run() {
	defer c.watcher.Close()
	go c.watchFilesystem()
	for {
		err := c.connect()
		if err != nil {
			log.Error().Err(err).Msg("Connection failed, retrying in 5 seconds...")
			time.Sleep(5 * time.Second)
			continue
		}
		c.listenToServer()
		log.Warn().Msg("Disconnected from server. Attempting to reconnect...")
		time.Sleep(5 * time.Second)
	}
}

func (c *Client) connect() error {
	u, err := url.Parse(c.cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}
	log.Info().Str("addr", u.String()).Msg("Connecting to server...")
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}
	c.conn = conn
	log.Info().Str("addr", c.cfg.ServerAddr).Msg("Successfully connected to server")
	return nil
}

func (c *Client) listenToServer() {
	defer c.conn.Close()
	for {
		var wrapper common.MessageWrapper
		if err := c.conn.ReadJSON(&wrapper); err != nil {
			log.Error().Err(err).Msg("Error reading from server")
			return
		}
		c.setSyncing(true)
		switch wrapper.Type {
		case common.TypeManifest:
			c.handleManifest(wrapper.Payload)
		case common.TypeFileContent:
			c.handleFileContent(wrapper.Payload)
		case common.TypeFileOperation:
			c.handleFileOperation(wrapper.Payload)
		default:
			log.Warn().Str("type", string(wrapper.Type)).Msg("Received unknown message type from server")
		}
		c.setSyncing(false)
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

	// Compare server manifest with local manifest
	for path, serverHash := range msg.Files {
		serverFiles[path] = true
		localHash, exists := localManifest[path]
		if !exists || localHash != serverHash {
			toRequest = append(toRequest, path)
		}
	}

	// Remove local files not on server
	for path := range localManifest {
		if !serverFiles[path] {
			fullPath := filepath.Join(c.cfg.SyncDir, path)
			log.Info().Str("path", path).Msg("Removing local file not present on server")
			if err := os.RemoveAll(fullPath); err != nil {
				log.Error().Err(err).Str("path", fullPath).Msg("Failed to remove local file")
			}
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
		log.Error().Err(err).Str("path", fullPath).Msg("Failed to create parent directories")
		return
	}
	if err := os.WriteFile(fullPath, msg.Content, 0644); err != nil {
		log.Error().Err(err).Str("path", msg.Path).Msg("Failed to write file")
	}
}

func (c *Client) handleFileOperation(payload []byte) {
	var op common.FileOperationMessage
	if err := json.Unmarshal(payload, &op); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal file operation")
		return
	}
	log.Info().Str("op", string(op.Op)).Str("path", op.Path).Msg("Received file operation from server")
	fullPath := filepath.Join(c.cfg.SyncDir, op.Path)

	switch op.Op {
	case common.OpWrite:
		if op.IsDir {
			if err := os.MkdirAll(fullPath, 0755); err != nil {
				log.Error().Err(err).Str("path", op.Path).Msg("Failed to create directory from operation")
			}
			return
		}
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			log.Error().Err(err).Msg("Failed to create parent directories")
			return
		}
		if err := os.WriteFile(fullPath, op.Content, 0644); err != nil {
			log.Error().Err(err).Str("path", op.Path).Msg("Failed to write file from operation")
		}
	case common.OpRemove:
		if err := os.RemoveAll(fullPath); err != nil {
			log.Error().Err(err).Str("path", fullPath).Msg("Failed to remove file from operation")
		}
	}
}

func (c *Client) requestFiles(paths []string) {
	payload, _ := json.Marshal(common.FileRequestMessage{Paths: paths})
	msg := common.MessageWrapper{
		Type:    common.TypeFileRequest,
		Payload: payload,
	}
	c.sendMessage(msg)
}

func (c *Client) watchFilesystem() {
	// Add all subdirectories to the watcher
	filepath.Walk(c.cfg.SyncDir, func(path string, info os.FileInfo, err error) error {
		if info != nil && info.IsDir() {
			relPath, _ := filepath.Rel(c.cfg.SyncDir, path)
			if !c.ignorer.IsIgnored(relPath) {
				if err := c.watcher.Add(path); err != nil {
					log.Error().Err(err).Str("path", path).Msg("Failed to add path to watcher")
				}
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
				continue // Ignore events generated by own sync
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
	relPath, err := filepath.Rel(c.cfg.SyncDir, event.Name)
	if err != nil || c.ignorer.IsIgnored(relPath) {
		return
	}
	op := common.FileOperationMessage{Path: relPath}
	if event.Op&fsnotify.Remove == fsnotify.Remove || event.Op&fsnotify.Rename == fsnotify.Rename {
		op.Op = common.OpRemove
		c.watcher.Remove(event.Name) // Stop watching removed files/dirs
	} else if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
		info, err := os.Stat(event.Name)
		if err != nil {
			// File may have been removed quickly, ignore error
			return
		}
		op.Op = common.OpWrite
		if info.IsDir() {
			if event.Op&fsnotify.Create == fsnotify.Create {
				c.watcher.Add(event.Name)
				op.IsDir = true
			} else {
				return
			}
		} else {
			content, err := os.ReadFile(event.Name)
			if err != nil {
				log.Error().Err(err).Str("path", event.Name).Msg("Failed to read file for sending")
				return
			}
			op.Content = content
		}
	} else {
		return
	}
	log.Info().Str("op", string(op.Op)).Str("path", relPath).Msg("Detected local change, sending to server")
	payload, _ := json.Marshal(op)
	msg := common.MessageWrapper{
		Type:    common.TypeFileOperation,
		Payload: payload,
	}
	c.sendMessage(msg)
}

func (c *Client) sendMessage(message common.MessageWrapper) {
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()
	if c.conn != nil {
		if err := c.conn.WriteJSON(message); err != nil {
			log.Error().Err(err).Msg("Failed to send message to server")
		}
	}
}

func (c *Client) setSyncing(status bool) {
	c.syncingMutex.Lock()
	defer c.syncingMutex.Unlock()
	c.isSyncing = status
}
