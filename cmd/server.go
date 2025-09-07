package cmd

import (
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/tanq16/fs-entangle/internal/server"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run the fs-entangle server",
	Run:   runServer,
}

var (
	serverPort    int
	serverDir     string
	serverIgnores string
)

func init() {
	serverCmd.Flags().IntVarP(&serverPort, "port", "p", 8080, "Port for the server to listen on")
	serverCmd.Flags().StringVarP(&serverDir, "dir", "d", ".", "Directory to sync (server's source of truth)")
	serverCmd.Flags().StringVar(&serverIgnores, "ignore", "", "Comma-separated list of glob patterns to ignore (e.g., '.git/*,*.tmp')")
}

func runServer(cmd *cobra.Command, args []string) {
	log.Info().Int("port", serverPort).Str("directory", serverDir).Str("ignores", serverIgnores).Msg("Starting fs-entangle server")
	cfg := server.Config{
		Port:        serverPort,
		SyncDir:     serverDir,
		IgnorePaths: serverIgnores,
	}
	s, err := server.New(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize server")
	}
	if err := s.Run(); err != nil {
		log.Fatal().Err(err).Msg("Server exited with an error")
	}
}
