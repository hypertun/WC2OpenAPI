package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/user/wc2api/internal/config"
	"github.com/user/wc2api/internal/server"
)

var (
	configPath = flag.String("config", "config.json", "Path to configuration file")
	port       = flag.String("port", "5001", "Server port (overrides config)")
)

func main() {
	flag.Parse()

	// Setup logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Override port if provided via flag
	if *port != "" && *port != "5001" {
		cfg.Server.Port = *port
	}

	slog.Info("Starting wc2api",
		"version", "1.0.0",
		"port", cfg.Server.Port,
	)

	// Create and start server
	srv, err := server.New(cfg)
	if err != nil {
		slog.Error("Failed to create server", "error", err)
		os.Exit(1)
	}

	// Setup graceful shutdown
	done := make(chan bool, 1)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		slog.Info("Server is shutting down...")
		srv.Stop()
		done <- true
	}()

	// Start server
	slog.Info(fmt.Sprintf("Server listening on http://0.0.0.0:%s", cfg.Server.Port))
	if err := srv.Start(); err != nil {
		slog.Error("Server failed to start", "error", err)
		os.Exit(1)
	}

	<-done
	slog.Info("Server stopped")
}
