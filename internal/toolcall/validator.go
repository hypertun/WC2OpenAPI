package toolcall

import (
	"encoding/json"
	"fmt"
	"log/slog"
	providers "github.com/user/wc2api/internal/providers"
	"reflect"
	"regexp"
	"strings"
)

// ValidationError represents a single validation error
type ValidationError struct {
	ToolName   string      `json:"tool_name"`
	Parameter  string      `json:"parameter"`
	Expected   string      `json:"expected"`
	Actual     interface{} `json:"actual"`
	Message    string      `json:"message"`
	Severity   string      `json:"severity"` // "error" or "warning"
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("[%s] %s: %s (expected: %s, got: %v)",
		e.ToolName, e.Parameter, e.Message, e.Expected, e.Actual)
}

// ValidationResult contains all validation errors/warnings
type ValidationResult struct {
	Errors   []*ValidationError
	Warnings []*ValidationError
	IsValid  bool
}

func (r *ValidationResult) HasErrors() bool {
	return len(r.Errors) > 0
}

func (r *ValidationResult) HasWarnings() bool {
	return len(r.Warnings) > 0
}

func (r *ValidationResult) AddError(param, expected string, actual interface{}, msg string) {
	r.Errors = append(r.Errors, &ValidationError{
		ToolName:  "", // Set by caller
		Parameter: param,
		Expected:  expected,
		Actual:    actual,
		Message:   msg,
		Severity:  "error",
	})
	r.IsValid = false
}

func (r *ValidationResult) AddWarning(param, expected string, actual interface{}, msg string) {
	r.Warnings = append(r.Warnings, &ValidationError{
		ToolName:  "",
		Parameter: param,
		Expected:  expected,
		Actual:    actual,
		Message:   msg,
		Severity:  "warning",
	})
}

// JSONSchema represents a simplified JSON Schema for tool parameters
type JSONSchema struct {
	Type       string            `json:"type,omitempty"`
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string          `json:"required,omitempty"`
	Enum       []interface{}     `json:"enum,omitempty"`
	Items      *JSONSchema       `json:"items,omitempty"` // For arrays
	Minimum    *float64          `json:"minimum,omitempty"` // For numbers
	Maximum    *float64          `json:"maximum,omitempty"` // For numbers
	MinLength  *int              `json:"minLength,omitempty"` // For strings
	MaxLength  *int              `json:"maxLength,omitempty"` // For strings
	Pattern    string            `json:"pattern,omitempty"`   // Regex for strings
	Format     string            `json:"format,omitempty"`    // e.g., "email", "uri"
	AdditionalProperties bool      `json:"additionalProperties,omitempty"`
}

// Property defines a parameter property in JSON Schema
type Property struct {
	Type        string                 `json:"type,omitempty"`
	Description string                 `json:"description,omitempty"`
	Enum        []interface{}          `json:"enum,omitempty"`
	Items       *Property              `json:"items,omitempty"`     // For array items
	Properties  map[string]Property    `json:"properties,omitempty"` // For object type
	Required    []string               `json:"required,omitempty"`  // For object type
	Minimum     *float64               `json:"minimum,omitempty"`    // For numbers
	Maximum     *float64               `json:"maximum,omitempty"`    // For numbers
	MinLength   *int                   `json:"minLength,omitempty"`  // For strings
	MaxLength   *int                   `json:"maxLength,omitempty"`  // For strings
	Pattern     string                 `json:"pattern,omitempty"`    // Regex for strings
	Format      string                 `json:"format,omitempty"`     // e.g., "email", "uri"
}

// ParseSchema parses a JSON Schema string into a JSONSchema struct
func ParseSchema(schemaJSON string) (*JSONSchema, error) {
	if schemaJSON == "" {
		return nil, fmt.Errorf("empty schema")
	}

	var schema JSONSchema
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return nil, fmt.Errorf("invalid JSON schema: %w", err)
	}

	return &schema, nil
}

// ValidateToolCall validates tool call arguments against a JSON schema
// Returns validation result with errors and warnings
func ValidateToolCall(toolName string, input map[string]any, schemaJSON string) *ValidationResult {
	result := &ValidationResult{
		IsValid: true,
	}

	// Parse schema
	schema, err := ParseSchema(schemaJSON)
	if err != nil {
		result.AddError("", "", nil,
			fmt.Sprintf("Failed to parse tool schema: %v", err))
		return result
	}

	// Set tool name in all errors for context
	defer func() {
		for _, err := range result.Errors {
			err.ToolName = toolName
		}
		for _, warn := range result.Warnings {
			warn.ToolName = toolName
		}
	}()

	// Validate root must be object
	if schema.Type != "" && schema.Type != "object" {
		result.AddWarning("", "", nil,
			fmt.Sprintf("Schema type is '%s', expected 'object'", schema.Type))
		// Continue anyway - we can still validate as object
	}

	// Validate required fields
	if len(schema.Required) > 0 {
		missingRequired := validateRequiredFields(input, schema.Required)
		for _, field := range missingRequired {
			result.AddError(field, "required", nil,
				fmt.Sprintf("Required parameter '%s' is missing", field))
		}
	}

	// Validate each property
	if schema.Properties != nil {
		for propName, propSchema := range schema.Properties {
			value, exists := input[propName]
			if !exists {
				continue // Already handled by required check
			}

			// Validate this property
			validateProperty(value, propSchema, propName, result)
		}
	}

	// Check for additional properties not in schema
	if schema.AdditionalProperties == false && schema.Properties != nil {
		for key := range input {
			if _, exists := schema.Properties[key]; !exists {
				result.AddWarning(key, "known parameter", input[key],
					"Parameter not defined in tool schema")
			}
		}
	}

	return result
}

// validateProperty validates a single property value against its schema
func validateProperty(value interface{}, schema Property, path string, result *ValidationResult) {
	// Skip nil values
	if value == nil {
		return
	}

	// Track errors before validating this property to avoid cross-property short-circuit
	startErrors := len(result.Errors)

	// Validate type first - if this fails, skip further type-dependent checks
	validateType(value, schema.Type, path, result)
	if len(result.Errors) > startErrors {
		return // Type error for this property, skip further validation
	}

	// Validate enum if present
	if len(schema.Enum) > 0 {
		validateEnum(value, schema.Enum, path, result)
	}

	// Type-specific validations
	switch schema.Type {
	case "string":
		strVal := value.(string)
		validateString(strVal, schema, path, result)

	case "number", "integer":
		validateNumber(value, schema, path, result)

	case "array":
		validateArray(value, schema, path, result)

	case "object":
		validateObject(value, schema, path, result)
	}
}

// validateType checks if value matches expected type
func validateType(value interface{}, expectedType string, path string, result *ValidationResult) {
	if expectedType == "" {
		return // No type constraint
	}

	var actualType string
	switch reflect.TypeOf(value).Kind() {
	case reflect.String:
		actualType = "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		actualType = "integer"
	case reflect.Float32, reflect.Float64:
		actualType = "number"
	case reflect.Bool:
		actualType = "boolean"
	case reflect.Array, reflect.Slice:
		actualType = "array"
	case reflect.Map:
		actualType = "object"
	case reflect.Interface:
		if value == nil {
			actualType = "null"
		} else {
			actualType = "unknown"
		}
	default:
		actualType = "unknown"
	}

	// Special handling: integer vs number
	if expectedType == "integer" && (actualType == "integer" || (actualType == "number" && isWholeNumber(value))) {
		return
	}

	if actualType != expectedType {
		result.AddError(path, expectedType, value,
			fmt.Sprintf("Type mismatch: expected %s, got %s", expectedType, actualType))
	}
}

// isWholeNumber checks if a numeric value is a whole number
func isWholeNumber(value interface{}) bool {
	switch v := value.(type) {
	case int:
		return true
	case int8, int16, int32, int64:
		return true
	case float32:
		return float64(v) == float64(int64(v))
	case float64:
		return v == float64(int64(v))
	default:
		return false
	}
}

// validateString validates string-specific constraints
func validateString(value string, schema Property, path string, result *ValidationResult) {
	// MinLength
	if schema.MinLength != nil && len(value) < *schema.MinLength {
		result.AddError(path, "string", value,
			fmt.Sprintf("String length %d is less than minimum %d", len(value), *schema.MinLength))
	}

	// MaxLength
	if schema.MaxLength != nil && len(value) > *schema.MaxLength {
		result.AddError(path, "string", value,
			fmt.Sprintf("String length %d exceeds maximum %d", len(value), *schema.MaxLength))
	}

	// Pattern (regex)
	if schema.Pattern != "" {
		matched, err := regexp.MatchString(schema.Pattern, value)
		if err != nil {
			result.AddError(path, "string", value,
				fmt.Sprintf("Invalid regex pattern '%s': %v", schema.Pattern, err))
		} else if !matched {
			result.AddError(path, "string", value,
				fmt.Sprintf("String does not match pattern '%s'", schema.Pattern))
		}
	}

	// Format (email, uri, etc.)
	if schema.Format != "" {
		validateFormat(value, schema.Format, path, result)
	}
}

// validateFormat checks string format constraints
func validateFormat(value string, format string, path string, result *ValidationResult) {
	switch format {
	case "email":
		emailRegex := `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`
		if matched, _ := regexp.MatchString(emailRegex, value); !matched {
			result.AddError(path, "email", value, "Invalid email format")
		}
	case "uri", "url":
		// Simple URI validation
		if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
			result.AddWarning(path, "uri", value, "URI should start with http:// or https://")
		}
	case "date-time":
		// Basic ISO 8601 check
		if len(value) < 20 {
			result.AddWarning(path, "date-time", value, "Date-time format appears invalid")
		}
	// Add more formats as needed
	default:
		// Unknown format - log warning but don't fail
		slog.Debug("Unknown format constraint", "format", format, "path", path)
	}
}

// validateNumber validates numeric constraints
func validateNumber(value interface{}, schema Property, path string, result *ValidationResult) {
	var floatVal float64
	var isNum bool
	
	switch v := value.(type) {
	case int:
		floatVal = float64(v)
		isNum = true
	case float64:
		floatVal = v
		isNum = true
	case float32:
		floatVal = float64(v)
		isNum = true
	case int64:
		floatVal = float64(v)
		isNum = true
	default:
		// Type mismatch already caught by validateType
		return
	}
	
	if !isNum {
		result.AddError(path, "number", value, "Cannot convert to numeric type")
		return
	}

	// Minimum
	if schema.Minimum != nil && floatVal < *schema.Minimum {
		result.AddError(path, "number", value,
			fmt.Sprintf("Value %v is less than minimum %v", floatVal, *schema.Minimum))
	}

	// Maximum
	if schema.Maximum != nil && floatVal > *schema.Maximum {
		result.AddError(path, "number", value,
			fmt.Sprintf("Value %v exceeds maximum %v", floatVal, *schema.Maximum))
	}
}

// validateArray validates array constraints
func validateArray(value interface{}, schema Property, path string, result *ValidationResult) {
	arr, ok := value.([]interface{})
	if !ok {
		// Type mismatch already caught
		return
	}

	// Validate array items if schema provided
	if schema.Items != nil {
		for i, item := range arr {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			validateProperty(item, *schema.Items, itemPath, result)
		}
	}
}

// validateObject validates object constraints
func validateObject(value interface{}, schema Property, path string, result *ValidationResult) {
	obj, ok := value.(map[string]interface{})
	if !ok {
		// Type mismatch already caught
		return
	}

	// Check required fields within this object first
	if len(schema.Required) > 0 {
		missingRequired := validateRequiredFields(obj, schema.Required)
		for _, field := range missingRequired {
			result.AddError(fmt.Sprintf("%s.%s", path, field), "required", nil,
				fmt.Sprintf("Required parameter '%s' is missing", field))
		}
	}

	// If schema has nested properties, validate them
	if schema.Properties != nil {
		for key, val := range obj {
			propPath := fmt.Sprintf("%s.%s", path, key)
			if propSchema, exists := schema.Properties[key]; exists {
				validateProperty(val, propSchema, propPath, result)
			}
		}
	}
}

// validateEnum checks if value is in allowed enum values
func validateEnum(value interface{}, enums []interface{}, path string, result *ValidationResult) {
	for _, allowed := range enums {
		if reflect.DeepEqual(value, allowed) {
			return // Valid
		}
	}

	result.AddError(path, "enum", value,
		fmt.Sprintf("Value '%v' is not in allowed set: %v", value, enums))
}

// validateRequiredFields checks for missing required parameters
func validateRequiredFields(input map[string]any, required []string) []string {
	var missing []string
	for _, field := range required {
		if _, exists := input[field]; !exists {
			missing = append(missing, field)
		}
	}
	return missing
}

// -- Hard-coded fallback schemas for common built-in tools --

// GetBuiltInSchema returns a hard-coded schema for known tool names
// Returns empty string if tool is not a known built-in
func GetBuiltInSchema(toolName string) string {
	schemas := map[string]string{
		"Read": `{
			"type": "object",
			"properties": {
				"file_path": {"type": "string"}
			},
			"required": ["file_path"],
			"additionalProperties": false
		}`,
		"Write": `{
			"type": "object",
			"properties": {
				"file_path": {"type": "string"},
				"content": {"type": "string"}
			},
			"required": ["file_path", "content"],
			"additionalProperties": false
		}`,
		"Bash": `{
			"type": "object",
			"properties": {
				"command": {"type": "string"}
			},
			"required": ["command"],
			"additionalProperties": false
		}`,
		"AskUserQuestion": `{
			"type": "object",
			"properties": {
				"questions": {
					"type": "array",
					"items": {
						"type": "object",
						"properties": {
							"question": {"type": "string"},
							"header": {"type": "string"},
							"options": {
								"type": "array",
								"items": {
									"type": "object",
									"properties": {
										"label": {"type": "string"},
										"description": {"type": "string"}
									}
								}
							},
							"multiSelect": {"type": "boolean"}
						},
						"required": ["question"]
					}
				}
			},
			"required": ["questions"],
			"additionalProperties": false
		}`,
		"Agent": `{
			"type": "object",
			"properties": {
				"description": {"type": "string"},
				"prompt": {"type": "string"}
			}
		}`,
	}

	if schema, exists := schemas[toolName]; exists {
		return schema
	}
	return ""
}

// -- Validation with both runtime and fallback schemas --

// ValidateWithToolDef validates a tool call using a tool definition
// Accepts either a Tool (from providers.Tool) or a schema string
func ValidateWithToolDef(toolName string, input map[string]any, toolDef *ToolDefinition) *ValidationResult {
	result := &ValidationResult{IsValid: true}

	// Try runtime schema first
	if toolDef != nil && toolDef.Parameters != "" {
		result = ValidateToolCall(toolName, input, toolDef.Parameters)
	} else if schema := GetBuiltInSchema(toolName); schema != "" {
		// Try hard-coded fallback
		result = ValidateToolCall(toolName, input, schema)
	} else {
		// No schema available - warn about unknown tool, do basic structure validation
		result = validateBasicStructure(input)
		result.AddWarning("", "", nil,
			fmt.Sprintf("No schema available for tool '%s' - validation limited to basic structure", toolName))
	}

	return result
}

// ToolDefinition mirrors providers.Tool for validation purposes
type ToolDefinition struct {
	Description string `json:"description,omitempty"`
	Parameters  string `json:"parameters,omitempty"` // JSON Schema as string
}

// validateBasicStructure performs minimal validation when no schema is available
func validateBasicStructure(input map[string]any) *ValidationResult {
	result := &ValidationResult{IsValid: true}

	// Check that input is an object
	if input == nil {
		result.AddError("", "", nil, "Tool call arguments must be an object")
		return result
	}

	// No schema available, so we can't know which params are required
	// Just warn about potentially problematic values
	for key, val := range input {
		if val == nil {
			result.AddWarning(key, "non-null", nil, "Parameter value is null")
		}
	}

	return result
}

// ValidateToolCallsWithErrors validates a list of tool calls against their tool definitions.
// Returns a combined list of validation errors.
func ValidateToolCallsWithErrors(toolCalls []providers.ToolCall, tools []providers.Tool) []*ValidationError {
	var allErrors []*ValidationError

	if len(toolCalls) == 0 {
		return allErrors
	}

	// Build a map of tool name -> schema for quick lookup
	toolSchemas := make(map[string]string)
	if len(tools) > 0 {
		for _, tool := range tools {
			if tool.Function.Parameters != nil {
				// Parameters is map[string]interface{}, convert to JSON string
				jsonBytes, err := json.Marshal(tool.Function.Parameters)
				if err == nil {
					toolSchemas[tool.Function.Name] = string(jsonBytes)
				}
			}
		}
	}

	for _, tc := range toolCalls {
		toolName := tc.Function.Name
		argsJSON := tc.Function.Arguments

		// Parse the arguments JSON
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			allErrors = append(allErrors, &ValidationError{
				ToolName:  toolName,
				Parameter: "",
				Expected:  "valid JSON object",
				Actual:    argsJSON,
				Message:   "Invalid JSON arguments: " + err.Error(),
				Severity:  "error",
			})
			continue
		}

		// Find the tool schema
		schema, ok := toolSchemas[toolName]
		if !ok {
			// Try fallback schema
			schema = GetBuiltInSchema(toolName)
			if schema == "" {
				// No schema available, do basic validation and add warning
				result := validateBasicStructure(args)
				// Add a warning that no tool definition was found
				allErrors = append(allErrors, &ValidationError{
					ToolName:  toolName,
					Parameter: "",
					Expected:  "tool definition",
					Actual:    "nil",
					Message:   "No tool definition found, using basic validation only",
					Severity:  "warning",
				})
				for _, e := range result.Errors {
					e.ToolName = toolName
					allErrors = append(allErrors, e)
				}
				for _, w := range result.Warnings {
					w.ToolName = toolName
					allErrors = append(allErrors, w)
				}
				continue
			}
		}

		// Validate against schema
		result := ValidateToolCall(toolName, args, schema)
		for _, e := range result.Errors {
			allErrors = append(allErrors, e)
		}
		for _, w := range result.Warnings {
			allErrors = append(allErrors, w)
		}
	}

	return allErrors
}

// FormatValidationErrors formats a list of validation errors into a human-readable string.
func FormatValidationErrors(errors []*ValidationError) string {
	if len(errors) == 0 {
		return ""
	}

	toolErrors := make(map[string][]string)
	for _, err := range errors {
		if err == nil {
			continue
		}
		key := err.ToolName
		if key == "" {
			key = "unknown"
		}
		msg := fmt.Sprintf("  - `%s`: %s (expected: %s, got: %v)",
			err.Parameter, err.Message, err.Expected, err.Actual)
		toolErrors[key] = append(toolErrors[key], msg)
	}

	msg := "Tool call error correction required. " +
		"The following tool calls had parameter errors and were NOT executed:\n\n"
	for tool, errs := range toolErrors {
		msg += tool + ":\n"
		for _, e := range errs {
			msg += e + "\n"
		}
		msg += "\n"
	}
	msg += "Please retry with corrected parameters. " +
		"Ensure all required fields are provided with correct types."

	return msg
}
