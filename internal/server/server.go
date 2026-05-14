package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/user/wc2api/internal/config"
	"github.com/user/wc2api/internal/handlers"
	"github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/providers/deepseek"
	"github.com/user/wc2api/internal/providers/qwen"
	serverMiddleware "github.com/user/wc2api/internal/server/middleware"
)

// Server represents the HTTP server
type Server struct {
	config     *config.Config
	router     *chi.Mux
	httpServer *http.Server
	providers  []providers.Provider
	routeTo    func(model string) (providers.Provider, bool)
}

// New creates a new server instance
func New(cfg *config.Config) (*Server, error) {
	s := &Server{
		config:   cfg,
		router:   chi.NewRouter(),
		providers: []providers.Provider{},
	}

	// Initialize providers
	errors := []error{}

	if cfg.Provider.DeepSeek.Enabled {
		prov, err := deepseek.New(cfg.Provider.DeepSeek)
		if err != nil {
			slog.Error("failed to create deepseek provider", "error", err)
			errors = append(errors, fmt.Errorf("failed to create deepseek provider: %w", err))
		} else {
			s.providers = append(s.providers, prov)
		}
	}

	if cfg.Provider.Qwen.Enabled {
		prov, err := qwen.New(cfg.Provider.Qwen)
		if err != nil {
			slog.Error("failed to create qwen provider", "error", err)
			errors = append(errors, fmt.Errorf("failed to create qwen provider: %w", err))
		} else {
			s.providers = append(s.providers, prov)
		}
	}

	if len(s.providers) == 0 {
		if len(errors) > 0 {
			return nil, fmt.Errorf("no providers available: %v", errors)
		}
		return nil, fmt.Errorf("no providers enabled - enable at least one provider in config")
	}

	// Setup routing function
	s.routeTo = s.createRouter()

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

// createRouter creates a function that routes model names to providers
func (s *Server) createRouter() func(model string) (providers.Provider, bool) {
	// Build prefix map
	prefixToProvider := make(map[string]providers.Provider)
	for _, p := range s.providers {
		name := strings.ToLower(p.Name())
		prefixToProvider[name] = p
	}

	return func(model string) (providers.Provider, bool) {
		model = strings.ToLower(model)

		// Check prefixes
		for prefix, prov := range prefixToProvider {
			if strings.HasPrefix(model, prefix+"-") || model == prefix {
				return prov, true
			}
		}

		// Default: use first available provider
		if len(s.providers) > 0 {
			return s.providers[0], true
		}

		return nil, false
	}
}

// getAllModels aggregates models from all providers
func (s *Server) getAllModels() []providers.Model {
	allModels := []providers.Model{}
	for _, p := range s.providers {
		allModels = append(allModels, p.ListModels()...)
	}
	return allModels
}

// GetProviderByModel implements handlers.ProviderSelector
func (s *Server) GetProviderByModel(model string) (providers.Provider, bool) {
	return s.routeTo(model)
}

// ListModels implements handlers.ProviderSelector
func (s *Server) ListModels() []providers.Model {
	return s.getAllModels()
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
		r.Get("/models", handlers.ListModels(s))
		r.Post("/chat/completions", handlers.ChatCompletions(s))
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

	// Close all providers (e.g., browsers)
	for _, p := range s.providers {
		if err := p.Close(); err != nil {
			slog.Error("provider close error", "provider", p.Name(), "error", err)
		}
	}
}