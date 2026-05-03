package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/user/wc2api/internal/providers"
)

// HealthCheck returns a health check handler
func HealthCheck() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
			"version": "1.0.0",
		})
	}
}

// ListModels returns a handler for listing models
func ListModels(provider providers.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models := provider.ListModels()
		
		response := struct {
			Object string           `json:"object"`
			Data   []providers.Model `json:"data"`
		}{
			Object: "list",
			Data:   models,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}
}
