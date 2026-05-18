package qwencn

import (
	"encoding/json"
	"strings"
)

// modelMappings maps user-facing model names (without prefix) to Qwen CN internal model IDs.
// Based on Chat2API's MODEl_MAP and qwenConfig.modelMappings.
var modelMappings = map[string]string{
	"Qwen3":              "tongyi-qwen3-max-model-agent",
	"Qwen3-Max":          "tongyi-qwen3-max-model-agent",
	"Qwen3-Max-Thinking": "tongyi-qwen3-max-thinking-agent",
	"Qwen3-Plus":         "tongyi-qwen-plus-agent",
	"Qwen3.5-Plus":       "Qwen3.5-Plus",
	"Qwen3-Flash":        "qwen3-flash",
	"Qwen3-Coder":        "qwen3-coder-plus",
}

// stripModelPrefix removes the "qwen-cn-" prefix and "-nothinking" suffix from model names.
func stripModelPrefix(model string) string {
	// Remove -nothinking suffix if present
	if strings.HasSuffix(model, "-nothinking") {
		model = strings.TrimSuffix(model, "-nothinking")
	}

	// Strip qwen-cn- prefix
	model = strings.TrimPrefix(model, "qwen-cn-")
	model = strings.TrimPrefix(model, "qwencn-")

	return model
}

// mapModel converts a user-facing model name to the internal Qwen CN model ID.
// Checks the freecache-backed dynamic model ID map (from API) first, then
// falls back to the static mapping, and finally returns the base name as-is.
func (c *Client) mapModel(model string) string {
	base := stripModelPrefix(model)

	// Check dynamic mapping from freecache
	if c != nil {
		if data, err := c.modelCache.Get([]byte("idmap")); err == nil {
			var idMap map[string]string
			if json.Unmarshal(data, &idMap) == nil {
				if mapped, ok := idMap[base]; ok {
					return mapped
				}
			}
		}
	}

	// Fall back to static mapping
	if mapped, ok := modelMappings[base]; ok {
		return mapped
	}
	return base
}
