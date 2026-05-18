package toolcall

import (
	"regexp"
	"strings"
)

// Tool name obfuscation: maps client tool names to provider-safe aliases.
// This prevents providers like Qwen from intercepting custom tool names
// as built-in functions (which causes "Tool X does not exist" errors).
//
// Strategy:
//  1. Explicit aliases for high-value short names that collide with provider namespace
//  2. Generic fallback: all other tools get a `u_` prefix to guarantee isolation
//
// Outbound (to provider): client name → alias / u_ prefix
// Inbound (from provider): alias / u_ prefix → client original name

// toolNameAliases maps popular short names to safe aliases.
var toolNameAliases = map[string]string{
	"Read":         "fs_open_file",
	"Write":        "fs_put_file",
	"Edit":         "fs_patch_file",
	"Bash":         "shell_run",
	"Grep":         "text_search",
	"Glob":         "path_find",
	"NotebookEdit": "notebook_patch",
	"WebFetch":     "http_get_url",
	"WebSearch":    "web_query",
}

// reverseAliases is the inverse of toolNameAliases, built in init().
var reverseAliases = make(map[string]string)

const autoPrefix = "u_"

func init() {
	for k, v := range toolNameAliases {
		reverseAliases[v] = k
	}
}

// ObfuscateToolName converts a client tool name to a provider-safe alias.
func ObfuscateToolName(name string) string {
	if name == "" {
		return name
	}
	if alias, ok := toolNameAliases[name]; ok {
		return alias
	}
	if _, ok := reverseAliases[name]; ok {
		return name
	}
	if strings.HasPrefix(name, autoPrefix) {
		return name
	}
	return autoPrefix + name
}

// DeobfuscateToolName converts a provider-returned name back to the original client tool name.
func DeobfuscateToolName(name string) string {
	if name == "" {
		return name
	}
	if orig, ok := reverseAliases[name]; ok {
		return orig
	}
	if strings.HasPrefix(name, autoPrefix) {
		return name[len(autoPrefix):]
	}
	return name
}

// ObfuscateBareNames replaces bare occurrences of tool names in prompt text
// with their obfuscated aliases.
func ObfuscateBareNames(text string) string {
	if text == "" {
		return text
	}
	names := make([]string, 0, len(toolNameAliases))
	for k := range toolNameAliases {
		names = append(names, k)
	}
	// Sort by length descending to avoid partial matches
	for i := 0; i < len(names)-1; i++ {
		for j := i + 1; j < len(names); j++ {
			if len(names[i]) < len(names[j]) {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	pattern := `\b(` + strings.Join(names, "|") + `)\b`
	re := regexp.MustCompile(pattern)
	return re.ReplaceAllStringFunc(text, func(m string) string {
		return toolNameAliases[m]
	})
}
