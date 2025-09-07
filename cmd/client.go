package cmd

import (
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/tanq16/fs-entangle/internal/client"
)

var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Run the fs-entangle client",
	Run:   runClient,
}

var (
	serverAddr    string
	clientDir     string
	clientIgnores string
)

func init() {
	clientCmd.Flags().StringVarP(&serverAddr, "addr", "a", "ws://localhost:8080/ws", "Address of the fs-entangle server")
	clientCmd.Flags().StringVarP(&clientDir, "dir", "d", ".", "Directory to sync with the server")
	clientCmd.Flags().StringVar(&clientIgnores, "ignore", "", "Comma-separated list of glob patterns to ignore for local changes (e.g., 'node_modules/*,*.log')")
}

func runClient(cmd *cobra.Command, args []string) {
	log.Info().Str("server_address", serverAddr).Str("directory", clientDir).Str("ignores", clientIgnores).Msg("Starting fs-entangle client")
	cfg := client.Config{
		ServerAddr:  serverAddr,
		SyncDir:     clientDir,
		IgnorePaths: clientIgnores,
	}
	c, err := client.New(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize client")
	}
	c.Run()
}
