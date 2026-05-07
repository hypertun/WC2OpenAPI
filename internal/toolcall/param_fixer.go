package toolcall

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
)

var paramNameAliases = map[string]string{
	"path":     "file_path",
	"filename": "file_path",
	"cmd":      "command",
	"script":   "command",
	"text":     "content",
	"url":      "uri",
}

var arrayParams = map[string]bool{
	"questions": true,
	"options":   true,
	"files":     true,
	"items":     true,
	"messages":  true,
}

var toolParamDefaults = map[string]map[string]interface{}{
	"Bash": {
		"timeout": float64(30000),
	},
}

func CoerceValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case string:
		if val == "" {
			return val
		}
		switch strings.ToLower(val) {
		case "true", "yes", "y":
			return true
		case "false", "no", "n":
			return false
		}
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return float64(i)
		}
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
		return val
	case bool:
		return v
	case float64:
		return v
	case []interface{}:
		for i, item := range val {
			val[i] = CoerceValue(item)
		}
		return val
	case map[string]interface{}:
		for k, item := range val {
			val[k] = CoerceValue(item)
		}
		return val
	default:
		return v
	}
}

func ApplyNameMappings(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	for alias, canonical := range paramNameAliases {
		if val, hasAlias := input[alias]; hasAlias {
			if _, hasTarget := input[canonical]; !hasTarget {
				input[canonical] = val
				delete(input, alias)
				slog.Debug("Applied param name mapping", "from", alias, "to", canonical)
			}
		}
	}
	return input
}

func IsArrayParam(name string) bool {
	return arrayParams[name]
}

func FixStructure(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	for k, v := range input {
		if _, isArray := v.([]interface{}); !isArray {
			if arrayParams[k] {
				input[k] = []interface{}{v}
				slog.Debug("Wrapped scalar in array", "param", k)
			}
		}
		if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "{") {
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(strVal), &parsed); err == nil {
				input[k] = parsed
				slog.Debug("Parsed JSON string to object", "param", k)
			}
		}
		if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "[") {
			var parsed []interface{}
			if err := json.Unmarshal([]byte(strVal), &parsed); err == nil {
				input[k] = parsed
				slog.Debug("Parsed JSON string to array", "param", k)
			}
		}
	}
	return input
}

func GetDefaultValue(toolName, paramName string) (interface{}, bool) {
	if defaults, ok := toolParamDefaults[toolName]; ok {
		val, found := defaults[paramName]
		return val, found
	}
	return nil, false
}

func ApplyDefaults(toolName string, input map[string]interface{}) map[string]interface{} {
	if defaults, ok := toolParamDefaults[toolName]; ok {
		for param, defaultVal := range defaults {
			if _, exists := input[param]; !exists {
				input[param] = defaultVal
				slog.Debug("Applied default value", "tool", toolName, "param", param, "value", defaultVal)
			}
		}
	}
	return input
}

func FixParameters(toolName string, input map[string]interface{}, schemaJSON string) map[string]interface{} {
	if input == nil {
		return nil
	}
	result := make(map[string]interface{})
	for k, v := range input {
		result[k] = v
	}
	for k, v := range result {
		result[k] = CoerceValue(v)
	}
	result = ApplyNameMappings(result)
	result = ApplyDefaults(toolName, result)
	result = FixStructure(result)
	return result
}
