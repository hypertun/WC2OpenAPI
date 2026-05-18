package qwencn

import (
	providers "github.com/user/wc2api/internal/providers"
)

// fallbackModels returns the hardcoded model list used when the API fetch fails.
func fallbackModels() []providers.Model {
	return []providers.Model{
		{ID: "qwen-cn-Qwen3", Object: "model", Created: 0, OwnedBy: "qwencn"},
		{ID: "qwen-cn-Qwen3-Max", Object: "model", Created: 0, OwnedBy: "qwencn"},
		{ID: "qwen-cn-Qwen3-Max-Thinking", Object: "model", Created: 0, OwnedBy: "qwencn"},
		{ID: "qwen-cn-Qwen3-Plus", Object: "model", Created: 0, OwnedBy: "qwencn"},
		{ID: "qwen-cn-Qwen3.5-Plus", Object: "model", Created: 0, OwnedBy: "qwencn"},
		{ID: "qwen-cn-Qwen3-Flash", Object: "model", Created: 0, OwnedBy: "qwencn"},
		{ID: "qwen-cn-Qwen3-Coder", Object: "model", Created: 0, OwnedBy: "qwencn"},
	}
}

// ListModels returns available models, fetched dynamically from the API.
// Falls back to a hardcoded list if the API is unreachable.
func (c *Client) ListModels() []providers.Model {
	return c.getModels()
}
