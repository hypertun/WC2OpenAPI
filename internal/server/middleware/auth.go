package middleware

import (
	"net/http"
	"strings"
)

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

			next.ServeHTTP(w, r)
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
