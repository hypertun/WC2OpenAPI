package toolcall

import (
	"encoding/json"
	"strings"
	"testing"

	providers "github.com/user/wc2api/internal/providers"
)

func TestParseSchema(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		wantErr bool
	}{
		{
			name: "valid object schema",
			schema: `{
				"type": "object",
				"properties": {
					"name": {"type": "string"}
				}
			}`,
			wantErr: false,
		},
		{
			name:    "empty schema",
			schema:  "",
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			schema:  `{invalid json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSchema(tt.schema)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseSchema(%q) expected error, got nil", tt.schema)
				}
			} else {
				if err != nil {
					t.Errorf("ParseSchema(%q) unexpected error: %v", tt.schema, err)
				}
			}
		})
	}
}

func TestValidateToolCall_RequiredFields(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"file_path": {"type": "string"},
			"content": {"type": "string"}
		},
		"required": ["file_path"]
	}`

	input := map[string]any{
		"content": "test content",
		// file_path missing
	}

	result := ValidateToolCall("TestTool", input, schema)
	if !result.HasErrors() {
		t.Error("Expected validation errors for missing required field")
	}
	if result.IsValid {
		t.Error("Expected validation to fail")
	}
	if len(result.Errors) == 0 {
		t.Error("Expected at least one error")
	} else {
		msg := result.Errors[0].Message
		if !strings.Contains(msg, "required") && !strings.Contains(msg, "file_path") {
			t.Errorf("Error message should mention required field, got: %s", msg)
		}
	}
}

func TestValidateToolCall_TypeMismatch(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"count": {"type": "integer"}
		},
		"required": ["count"]
	}`

	input := map[string]any{
		"count": "not a number",
	}

	result := ValidateToolCall("TestTool", input, schema)
	if !result.HasErrors() {
		t.Error("Expected type mismatch error")
	}
	if len(result.Errors) == 0 {
		t.Error("Expected at least one error")
	} else {
		msg := result.Errors[0].Message
		if !contains(msg, "Type mismatch") {
			t.Errorf("Expected type mismatch error, got: %s", msg)
		}
	}
}

func TestValidateToolCall_ValidInput(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"age": {"type": "integer"}
		},
		"required": ["name"]
	}`

	input := map[string]any{
		"name": "John",
		"age":  30,
	}

	result := ValidateToolCall("TestTool", input, schema)
	if result.HasErrors() {
		t.Errorf("Expected no errors, got: %v", result.Errors)
	}
	if !result.IsValid {
		t.Error("Expected validation to succeed")
	}
}

func TestValidateToolCall_StringConstraints(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		input    map[string]any
		wantErr  bool
		errMsg   string
	}{
		{
			name: "minLength violation",
			schema: `{
				"type": "object",
				"properties": {
					"name": {"type": "string", "minLength": 5}
				}
			}`,
			input:   map[string]any{"name": "ab"},
			wantErr: true,
			errMsg:  "less than minimum",
		},
		{
			name: "maxLength violation",
			schema: `{
				"type": "object",
				"properties": {
					"name": {"type": "string", "maxLength": 3}
				}
			}`,
			input:   map[string]any{"name": "abcdef"},
			wantErr: true,
			errMsg:  "exceeds maximum",
		},
		{
			name: "email format",
			schema: `{
				"type": "object",
				"properties": {
					"email": {"type": "string", "format": "email"}
				}
			}`,
			input:  map[string]any{"email": "not-an-email"},
			wantErr: true,
			errMsg:  "Invalid email format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateToolCall("TestTool", tt.input, tt.schema)
			if tt.wantErr {
				if !result.HasErrors() {
					t.Errorf("Expected error but got none")
					return
				}
				found := false
				for _, err := range result.Errors {
					if contains(err.Message, tt.errMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected error message containing '%s', got: %v", tt.errMsg, result.Errors)
				}
			} else {
				if result.HasErrors() {
					t.Errorf("Expected no errors, got: %v", result.Errors)
				}
			}
		})
	}
}

func TestValidateToolCall_NumberConstraints(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		input   map[string]any
		wantErr bool
	}{
		{
			name: "minimum violation",
			schema: `{
				"type": "object",
				"properties": {
					"count": {"type": "number", "minimum": 10}
				}
			}`,
			input:   map[string]any{"count": 5},
			wantErr: true,
		},
		{
			name: "maximum violation",
			schema: `{
				"type": "object",
				"properties": {
					"count": {"type": "number", "maximum": 10}
				}
			}`,
			input:   map[string]any{"count": 15},
			wantErr: true,
		},
		{
			name: "integer type check",
			schema: `{
				"type": "object",
				"properties": {
					"count": {"type": "integer"}
				}
			}`,
			input:   map[string]any{"count": 42},
			wantErr: false,
		},
		{
			name: "integer accepts whole number float",
			schema: `{
				"type": "object",
				"properties": {
					"count": {"type": "integer"}
				}
			}`,
			input:   map[string]any{"count": 42.0},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateToolCall("TestTool", tt.input, tt.schema)
			if tt.wantErr {
				if !result.HasErrors() {
					t.Errorf("Expected error but got none")
				}
			} else {
				if !result.IsValid {
					t.Errorf("Expected valid but got errors: %v", result.Errors)
				}
			}
		})
	}
}

func TestValidateToolCall_Enum(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"level": {"type": "string", "enum": ["low", "medium", "high"]}
		}
	}`

	input := map[string]any{
		"level": "invalid",
	}

	result := ValidateToolCall("TestTool", input, schema)
	if !result.HasErrors() {
		t.Error("Expected enum validation error")
	}
	if len(result.Errors) == 0 {
		t.Error("Expected at least one error")
	} else {
		msg := result.Errors[0].Message
		if !strings.Contains(msg, "not in allowed set") {
			t.Errorf("Expected enum error, got: %s", msg)
		}
	}
}

func TestValidateToolCall_ArrayValidation(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"items": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"id": {"type": "integer"},
						"name": {"type": "string"}
					},
					"required": ["id"]
				}
			}
		}
	}`

	input := map[string]any{
		"items": []interface{}{
			map[string]interface{}{"id": 1, "name": "first"},
			map[string]interface{}{"id": "not-int", "name": "second"},
			map[string]interface{}{}, // missing required id
		},
	}

	result := ValidateToolCall("TestTool", input, schema)
	if !result.HasErrors() {
		t.Error("Expected array item validation errors")
	}
	// Should have at least 2 errors (type mismatch + missing required)
	if len(result.Errors) < 2 {
		t.Errorf("Expected at least 2 errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestValidateWithToolDef_RuntimeSchema(t *testing.T) {
	toolDef := &ToolDefinition{
		Parameters: `{
			"type": "object",
			"properties": {
				"name": {"type": "string"}
			},
			"required": ["name"]
		}`,
	}

	input := map[string]any{
		"name": "test",
	}

	result := ValidateWithToolDef("TestTool", input, toolDef)
	if !result.IsValid {
		t.Errorf("Expected valid result, got errors: %v", result.Errors)
	}
}

func TestValidateWithToolDef_FallbackSchema(t *testing.T) {
	input := map[string]any{
		"command": "ls -la",
	}

	result := ValidateWithToolDef("Bash", input, nil)
	if !result.IsValid {
		t.Errorf("Expected valid result for Bash tool, got errors: %v", result.Errors)
	}
}

func TestValidateWithToolDef_UnknownTool(t *testing.T) {
	input := map[string]any{
		"some": "value",
	}

	result := ValidateWithToolDef("UnknownTool", input, nil)
	if !result.HasWarnings() {
		t.Error("Expected warning for unknown tool")
	}
}

func TestGetBuiltInSchema(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     bool
	}{
		{"Read", "Read", true},
		{"Write", "Write", true},
		{"Bash", "Bash", true},
		{"AskUserQuestion", "AskUserQuestion", true},
		{"Agent", "Agent", true},
		{"Unknown", "SomeOtherTool", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := GetBuiltInSchema(tt.toolName)
			if tt.want {
				if schema == "" {
					t.Errorf("Expected schema for %s, got empty string", tt.toolName)
					return
				}
				// Verify it's valid JSON
				var jsonMap map[string]interface{}
				err := json.Unmarshal([]byte(schema), &jsonMap)
				if err != nil {
					t.Errorf("Schema for %s is not valid JSON: %v", tt.toolName, err)
				}
			} else {
				if schema != "" {
					t.Errorf("Expected no schema for %s, got: %s", tt.toolName, schema)
				}
			}
		})
	}
}

func TestValidateToolCall_AdditionalProperties(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"name": {"type": "string"}
		},
		"additionalProperties": false
	}`

	input := map[string]any{
		"name":   "test",
		"extra":  "not in schema",
	}

	result := ValidateToolCall("TestTool", input, schema)
	if !result.HasWarnings() {
		t.Error("Expected warning for additional property")
	}
}

func TestValidateToolCall_NullValues(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"optional": {"type": "string"}
		},
		"required": ["name"]
	}`

	input := map[string]any{
		"name":    "test",
		"optional": nil,
	}

	result := ValidateToolCall("TestTool", input, schema)
	// Should be valid (null allowed for optional fields)
	if result.HasErrors() {
		t.Errorf("Expected no errors for optional null, got: %v", result.Errors)
	}
}

func TestValidateToolCall_ArrayItemsRequired(t *testing.T) {
	// Test required validation inside array items
	schema := `{
		"type": "object",
		"properties": {
			"items": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"id": {"type": "integer"},
						"name": {"type": "string"}
					},
					"required": ["id"]
				}
			}
		}
	}`

	// Debug: Parse schema to check required field
	parsed, err := ParseSchema(schema)
	if err != nil {
		t.Fatalf("Failed to parse schema: %v", err)
	}
	if itemsProp, ok := parsed.Properties["items"]; ok {
		if itemsProp.Items != nil {
			t.Logf("Items schema Required: %v", itemsProp.Items.Required)
		} else {
			t.Log("ItemsProp.Items is nil")
		}
	}

	input := map[string]any{
		"items": []interface{}{
			map[string]interface{}{"id": 1, "name": "first"}, // valid
			map[string]interface{}{"id": "not-int"},           // type error on id
			map[string]interface{}{},                         // missing id
		},
	}

	result := ValidateToolCall("TestTool", input, schema)
	if !result.HasErrors() {
		t.Fatal("Expected validation errors for array items")
	}
	// Should have at least 2 errors: type mismatch and missing required
	if len(result.Errors) < 2 {
		t.Errorf("Expected at least 2 errors, got %d: %v", len(result.Errors), result.Errors)
	}
	// Verify we have an error about missing id
	missingID := false
	typeErr := false
	for _, err := range result.Errors {
		if strings.Contains(err.Message, "Required parameter") || strings.Contains(err.Message, "missing") {
			if strings.Contains(err.Parameter, "id") {
				missingID = true
			}
		}
		if strings.Contains(err.Message, "Type mismatch") {
			typeErr = true
		}
	}
	if !missingID {
		t.Errorf("Expected error about missing required 'id', got: %v", result.Errors)
	}
	if !typeErr {
		t.Errorf("Expected type mismatch error, got: %v", result.Errors)
	}
}

func TestValidateToolCall_NestedObjectRequired(t *testing.T) {
	// Test that required fields within nested objects are validated
	schema := `{
		"type": "object",
		"properties": {
			"config": {
				"type": "object",
				"properties": {
					"id": {"type": "integer"},
					"name": {"type": "string"}
				},
				"required": ["id"]
			}
		}
	}`

	input := map[string]any{
		"config": map[string]interface{}{}, // empty nested object, missing required id
	}

	result := ValidateToolCall("TestTool", input, schema)
	if !result.HasErrors() {
		t.Fatal("Expected missing required error for nested object")
	}
	// Verify error mentions 'id'
	found := false
	for _, err := range result.Errors {
		if strings.Contains(err.Parameter, "id") || strings.Contains(err.Message, "id") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected error about missing 'id', got: %v", result.Errors)
	}
}

// Helper to create a tool for testing
func makeTestTool(name, schemaStr string) providers.Tool {
	var params map[string]interface{}
	json.Unmarshal([]byte(schemaStr), &params)
	return providers.Tool{
		Type: "function",
		Function: providers.ToolFunction{
			Name:        name,
			Description: "Test tool: " + name,
			Parameters:  params,
		},
	}
}

func TestValidateToolCallsWithErrors_NoErrors(t *testing.T) {
	tools := []providers.Tool{
		makeTestTool("Bash", `{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
	}
	toolCalls := []providers.ToolCall{
		{ID: "1", Type: "function", Function: providers.ToolCallFunction{Name: "Bash", Arguments: `{"command": "ls -la"}`}},
	}

	errs := ValidateToolCallsWithErrors(toolCalls, tools)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(errs), errs)
	}
}

func TestValidateToolCallsWithErrors_MultipleTools(t *testing.T) {
	tools := []providers.Tool{
		makeTestTool("Bash", `{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
		makeTestTool("Read", `{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
	}
	toolCalls := []providers.ToolCall{
		{ID: "1", Type: "function", Function: providers.ToolCallFunction{Name: "Bash", Arguments: `{"command": "ls"}`}},
		{ID: "2", Type: "function", Function: providers.ToolCallFunction{Name: "Read", Arguments: `{}`}}, // missing file_path
	}

	errs := ValidateToolCallsWithErrors(toolCalls, tools)
	if len(errs) == 0 {
		t.Fatal("expected errors for missing required param")
	}
	found := false
	for _, e := range errs {
		if e.ToolName == "Read" && strings.Contains(e.Parameter, "file_path") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error for Read missing file_path, got: %v", errs)
	}
}

func TestValidateToolCallsWithErrors_NilTools(t *testing.T) {
	toolCalls := []providers.ToolCall{
		{ID: "1", Type: "function", Function: providers.ToolCallFunction{Name: "UnknownTool", Arguments: `{"param": "value"}`}},
	}

	errs := ValidateToolCallsWithErrors(toolCalls, nil)
	if len(errs) == 0 {
		t.Error("expected warning for nil tools with unknown tool")
	}
}

func TestFormatValidationErrors_Empty(t *testing.T) {
	msg := FormatValidationErrors([]*ValidationError{})
	if msg != "" {
		t.Errorf("expected empty string, got %q", msg)
	}
}

func TestFormatValidationErrors_Single(t *testing.T) {
	errs := []*ValidationError{
		{ToolName: "Bash", Parameter: "command", Message: "required", Severity: "error"},
	}
	msg := FormatValidationErrors(errs)
	if !strings.Contains(msg, "Bash") {
		t.Errorf("expected Bash in formatted errors, got: %q", msg)
	}
}

func TestFormatValidationErrors_Multiple(t *testing.T) {
	errs := []*ValidationError{
		{ToolName: "Bash", Parameter: "command", Message: "required", Severity: "error"},
		{ToolName: "Read", Parameter: "file_path", Message: "required", Severity: "error"},
	}
	msg := FormatValidationErrors(errs)
	if !strings.Contains(msg, "Bash") || !strings.Contains(msg, "Read") {
		t.Errorf("expected both tool names in formatted errors, got: %q", msg)
	}
}

func TestValidationResult_HasErrors_True(t *testing.T) {
	r := &ValidationResult{
		IsValid: false,
		Errors:  []*ValidationError{{Message: "test"}},
	}
	if !r.HasErrors() {
		t.Error("expected HasErrors() to return true")
	}
}

func TestValidationResult_HasWarnings_True(t *testing.T) {
	r := &ValidationResult{
		IsValid:  true,
		Warnings: []*ValidationError{{Message: "test", Severity: "warning"}},
	}
	if !r.HasWarnings() {
		t.Error("expected HasWarnings() to return true")
	}
}
