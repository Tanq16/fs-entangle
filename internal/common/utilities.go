package common

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/rs/zerolog/log"
)

type PathIgnorer struct {
	patterns []string
}

func NewPathIgnorer(ignoreStr string) *PathIgnorer {
	if ignoreStr == "" {
		return &PathIgnorer{patterns: []string{}}
	}
	patterns := strings.Split(ignoreStr, ",")
	// Add gitignore-style directory matching
	for i, p := range patterns {
		if !strings.HasSuffix(p, "**") {
			patterns[i] = strings.TrimSuffix(p, "/") + "/**"
		}
	}
	return &PathIgnorer{patterns: patterns}
}

func (pi *PathIgnorer) IsIgnored(path string) bool {
	for _, pattern := range pi.patterns {
		match, err := doublestar.Match(pattern, path)
		if err == nil && match {
			return true
		}
	}
	return false
}

func ComputeFileHash(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func BuildFileManifest(rootDir string, ignorer *PathIgnorer) (map[string]string, error) {
	manifest := make(map[string]string)
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(rootDir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}
		if ignorer.IsIgnored(relPath) {
			log.Debug().Str("path", relPath).Msg("Ignoring path based on rules")
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() {
			hash, err := ComputeFileHash(path)
			if err != nil {
				log.Error().Err(err).Str("path", path).Msg("Failed to compute hash for file")
				return nil // Continue walking even if one file fails
			}
			manifest[relPath] = hash
		}
		return nil
	})
	return manifest, err
}
