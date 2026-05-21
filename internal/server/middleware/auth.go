package middleware

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const APIKeyContextKey contextKey = "api_key"

// Auth creates authentication middleware
func Auth(apiKeys []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip auth if no API keys configured (development mode)
			if len(apiKeys) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			// Extract API key from header
			authHeader := r.Header.Get("Authorization")
			var key string
			
			if strings.HasPrefix(authHeader, "Bearer ") {
				key = strings.TrimPrefix(authHeader, "Bearer ")
			} else {
				key = r.Header.Get("X-Api-Key")
			}

		// Validate API key
		if !isValidKey(key, apiKeys) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "Invalid API key"}`))
			return
		}

		// Store API key in context
		ctx := context.WithValue(r.Context(), APIKeyContextKey, key)
		next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isValidKey checks if the provided key is in the valid keys list
func isValidKey(key string, validKeys []string) bool {
	for _, validKey := range validKeys {
		if key == validKey {
			return true
		}
	}
	return false
}

// GetAPIKey retrieves the API key from the request context.
func GetAPIKey(r *http.Request) string {
	if v, ok := r.Context().Value(APIKeyContextKey).(string); ok {
		return v
	}
	return ""
}
