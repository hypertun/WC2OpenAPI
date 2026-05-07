package qwen

import (
	"regexp"
	"strings"
)

// Tool name obfuscation: maps client tool names to Qwen-safe aliases to avoid
// upstream interception where Qwen treats custom tool names as built-in functions
// and returns errors like "Tool X does not exists."
//
// Strategy:
//  1. Explicit aliases for high-value short names that collide with Qwen's namespace
//  2. Generic fallback: all other tools get a `u_` prefix to guarantee isolation
//
// Outbound (to Qwen): client name → alias / u_ prefix
// Inbound (from Qwen): alias / u_ prefix → client original name

// Explicit aliases: popular short names that Qwen's content filters likely flag
var toolNameAliases = map[string]string{
	"Read":          "fs_open_file",
	"Write":         "fs_put_file",
	"Edit":          "fs_patch_file",
	"Bash":          "shell_run",
	"Grep":          "text_search",
	"Glob":          "path_find",
	"NotebookEdit":  "notebook_patch",
	"WebFetch":      "http_get_url",
	"WebSearch":     "web_query",
}

// Reverse lookup: alias → original
var reverseAliases = make(map[string]string)

// autoPrefix is prepended to any tool name without an explicit alias
const autoPrefix = "u_"

func init() {
	for k, v := range toolNameAliases {
		reverseAliases[v] = k
	}
}

// toQwenName converts a client tool name to a Qwen-safe alias.
//   - If the name has an explicit alias, returns the alias (e.g., Read → fs_open_file)
//   - If the name already is an alias or has the u_ prefix, returns it unchanged
//   - Otherwise, prepends u_ (e.g., TaskCreate → u_TaskCreate)
func toQwenName(name string) string {
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

// fromQwenName converts a Qwen-returned name back to the original client tool name.
//   - If the name is a known alias, reverse-lookup to the original (fs_open_file → Read)
//   - If the name has the u_ prefix, strip it (u_TaskCreate → TaskCreate)
//   - Otherwise, return as-is (Qwen may occasionally echo the original)
func fromQwenName(name string) string {
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

// obfuscateBareNames replaces bare occurrences of tool names in prompt text
// with their obfuscated aliases. Only the explicitly-aliased names are replaced
// (Read, Write, Edit, Bash, Grep, Glob, NotebookEdit, WebFetch, WebSearch), since
// those are the ones likely to appear as naked words in free-form instructions.
func obfuscateBareNames(text string) string {
	if text == "" {
		return text
	}
	// Build a regex that matches any alias name as a whole word.
	// Sort by length descending to avoid partial matches (e.g., "Read" inside "Readability").
	names := make([]string, 0, len(toolNameAliases))
	for k := range toolNameAliases {
		names = append(names, k)
	}
	// Sort by length descending
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
