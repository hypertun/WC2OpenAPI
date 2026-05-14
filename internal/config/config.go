package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config represents the application configuration
type Config struct {
	Server   ServerConfig   `json:"server"`
	Auth     AuthConfig     `json:"auth"`
	Provider ProviderConfig `json:"provider"`
}

// ServerConfig holds HTTP server settings
// ReadTimeout and WriteTimeout are integers (seconds) in config.json per AGENTS.md
type ServerConfig struct {
	Host         string `json:"host"`
	Port         string `json:"port"`
	ReadTimeout  int    `json:"read_timeout"`  // seconds
	WriteTimeout int    `json:"write_timeout"` // seconds
}

// AuthConfig holds authentication settings
type AuthConfig struct {
	APIKeys []string `json:"api_keys"`
}

// ProviderConfig holds provider-specific settings
type ProviderConfig struct {
	DeepSeek DeepSeekConfig `json:"deepseek"`
Qwen     QwenConfig     `json:"qwen"`
}

// DeepSeekConfig holds DeepSeek provider settings
// Timeout and TokenRefreshInterval are integers (seconds) in config.json per AGENTS.md
type DeepSeekConfig struct {
	Enabled              bool   `json:"enabled"`
	Email                string `json:"email"`
	Password             string `json:"password,omitempty"`
	BaseURL              string `json:"base_url"`
	LoginURL             string `json:"login_url"`
	CreateSessionURL     string `json:"create_session_url"`
	Timeout              int    `json:"timeout"`                // seconds
	TokenRefreshInterval int    `json:"token_refresh_interval"` // seconds
}

// QwenConfig holds Qwen provider settings
// Timeout and TokenRefreshInterval are integers (seconds) in config.json per AGENTS.md
type QwenConfig struct {
	Enabled              bool   `json:"enabled"`
	Email                string `json:"email"`
	Password             string `json:"password,omitempty"`
	BaseURL              string `json:"base_url"`
	Timeout              int    `json:"timeout"`                // seconds
	TokenRefreshInterval int    `json:"token_refresh_interval"` // seconds
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host:         "0.0.0.0",
			Port:         "5001",
			ReadTimeout:  30, // seconds per AGENTS.md
			WriteTimeout: 60, // seconds per AGENTS.md
		},
		Auth: AuthConfig{
			APIKeys: []string{},
		},
		Provider: ProviderConfig{
			DeepSeek: DeepSeekConfig{
				Enabled:              true,
				BaseURL:              "https://chat.deepseek.com",
				LoginURL:             "/api/v0/users/login",
				CreateSessionURL:     "/api/v0/chat/create_session",
				Timeout:              120,  // seconds per AGENTS.md
				TokenRefreshInterval: 1800, // 30 minutes in seconds
			},
			Qwen: QwenConfig{
				Enabled:              false,
				BaseURL:              "https://chat.qwen.ai",
				Timeout:              120,  // seconds per AGENTS.md
				TokenRefreshInterval: 1800, // 30 minutes in seconds
			},
		},
	}
}

// Load reads configuration from file
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	// Try to load from config file
	if _, err := os.Stat(path); err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.Server.Port == "" {
		return fmt.Errorf("server port is required")
	}

	if c.Provider.DeepSeek.Enabled {
		if c.Provider.DeepSeek.Email == "" {
			return fmt.Errorf("deepseek email is required when deepseek is enabled")
		}
		if c.Provider.DeepSeek.Password == "" {
			return fmt.Errorf("deepseek password is required when deepseek is enabled")
		}
	}

	if c.Provider.Qwen.Enabled {
		if c.Provider.Qwen.Email == "" {
			return fmt.Errorf("qwen email is required when qwen is enabled")
		}
		if c.Provider.Qwen.Password == "" {
			return fmt.Errorf("qwen password is required when qwen is enabled")
		}
	}
	return nil
}