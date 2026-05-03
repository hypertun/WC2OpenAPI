package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/user/wc2api/internal/config"
	"github.com/user/wc2api/internal/handlers"
	"github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/providers/deepseek"
	serverMiddleware "github.com/user/wc2api/internal/server/middleware"
)

// Server represents the HTTP server
type Server struct {
	config     *config.Config
	router     *chi.Mux
	httpServer *http.Server
	provider   providers.Provider
}

// New creates a new server instance
func New(cfg *config.Config) (*Server, error) {
	s := &Server{
		config: cfg,
		router: chi.NewRouter(),
	}

	// Initialize provider
	if cfg.Provider.DeepSeek.Enabled {
		prov, err := deepseek.New(cfg.Provider.DeepSeek)
		if err != nil {
			return nil, fmt.Errorf("failed to create deepseek provider: %w", err)
		}
		s.provider = prov
	}

	s.setupMiddleware()
	s.setupRoutes()

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port),
		Handler:      s.router,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(cfg.Server.WriteTimeout) * time.Second,
	}

	return s, nil
}

// setupMiddleware configures all middleware
func (s *Server) setupMiddleware() {
	// Basic middleware
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	s.router.Use(serverMiddleware.Logger)
	s.router.Use(middleware.Recoverer)

	// CORS
	s.router.Use(serverMiddleware.CORS())

	// Request timeout
	s.router.Use(middleware.Timeout(time.Duration(s.config.Server.WriteTimeout) * time.Second))
}

// setupRoutes configures all routes
func (s *Server) setupRoutes() {
	// Health check (no auth required)
	s.router.Get("/healthz", handlers.HealthCheck())

	// API routes (require auth)
	s.router.Route("/v1", func(r chi.Router) {
		r.Use(serverMiddleware.Auth(s.config.Auth.APIKeys))

		// OpenAI compatible endpoints
		r.Get("/models", handlers.ListModels(s.provider))
		r.Post("/chat/completions", handlers.ChatCompletions(s.provider))
	})
}

// Start starts the HTTP server
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Stop gracefully shuts down the server
func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.httpServer.Shutdown(ctx)
}
