package toolcall

import (
	"testing"
)

func TestCoerceValue_StringToBool(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected interface{}
	}{
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"y", true},
		{"false", false},
		{"FALSE", false},
		{"no", false},
		{"n", false},
	}
	for _, tt := range tests {
		got := CoerceValue(tt.input)
		if got != tt.expected {
			t.Errorf("CoerceValue(%v) = %v (type %T), want %v (type %T)", tt.input, got, got, tt.expected, tt.expected)
		}
	}
}

func TestCoerceValue_StringToNumber(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected float64
	}{
		{"123", 123},
		{"3.14", 3.14},
		{"-42", -42},
	}
	for _, tt := range tests {
		got := CoerceValue(tt.input)
		f, ok := got.(float64)
		if !ok {
			t.Errorf("CoerceValue(%v) = %v (type %T), want float64", tt.input, got, got)
			continue
		}
		if f != tt.expected {
			t.Errorf("CoerceValue(%v) = %v, want %v", tt.input, f, tt.expected)
		}
	}
}

func TestCoerceValue_StringPreserved(t *testing.T) {
	got := CoerceValue("hello")
	s, ok := got.(string)
	if !ok || s != "hello" {
		t.Errorf("CoerceValue(\"hello\") = %v (type %T), want string \"hello\"", got, got)
	}
}

func TestCoerceValue_NestedMap(t *testing.T) {
	input := map[string]interface{}{
		"count": "42",
		"nested": map[string]interface{}{
			"active": "true",
		},
	}
	got := CoerceValue(input)
	m, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	if m["count"].(float64) != 42 {
		t.Errorf("expected count=42, got %v", m["count"])
	}
	nested, _ := m["nested"].(map[string]interface{})
	if nested["active"] != true {
		t.Errorf("expected active=true, got %v", nested["active"])
	}
}

func TestCoerceValue_NestedArray(t *testing.T) {
	input := []interface{}{"42", "false", "hello"}
	got := CoerceValue(input)
	arr, ok := got.([]interface{})
	if !ok {
		t.Fatalf("expected []interface{}, got %T", got)
	}
	if arr[0].(float64) != 42 {
		t.Errorf("expected arr[0]=42, got %v", arr[0])
	}
	if arr[1] != false {
		t.Errorf("expected arr[1]=false, got %v", arr[1])
	}
	if arr[2].(string) != "hello" {
		t.Errorf("expected arr[2]=\"hello\", got %v", arr[2])
	}
}

func TestCoerceValue_Nil(t *testing.T) {
	if got := CoerceValue(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestApplyNameMappings(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name:     "path to file_path",
			input:    map[string]interface{}{"path": "/tmp/file"},
			expected: map[string]interface{}{"file_path": "/tmp/file"},
		},
		{
			name:     "filename to file_path",
			input:    map[string]interface{}{"filename": "test.txt"},
			expected: map[string]interface{}{"file_path": "test.txt"},
		},
		{
			name:     "cmd to command",
			input:    map[string]interface{}{"cmd": "ls"},
			expected: map[string]interface{}{"command": "ls"},
		},
		{
			name:     "script to command",
			input:    map[string]interface{}{"script": "echo hi"},
			expected: map[string]interface{}{"command": "echo hi"},
		},
		{
			name:     "text to content",
			input:    map[string]interface{}{"text": "hello world"},
			expected: map[string]interface{}{"content": "hello world"},
		},
		{
			name:     "no conflict with existing canonical — alias preserved",
			input:    map[string]interface{}{"path": "/p", "file_path": "/existing"},
			expected: map[string]interface{}{"file_path": "/existing", "path": "/p"},
		},
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyNameMappings(tt.input)
			if got == nil && tt.expected == nil {
				return
			}
			if got == nil || tt.expected == nil {
				t.Fatalf("mismatch: got %v, want %v", got, tt.expected)
			}
		for k, v := range tt.expected {
			if got[k] != v {
				t.Errorf("key %s: got %v, want %v", k, got[k], v)
			}
		}
		})
	}
}

func TestIsArrayParam(t *testing.T) {
	if !IsArrayParam("questions") {
		t.Error("expected 'questions' to be array param")
	}
	if !IsArrayParam("options") {
		t.Error("expected 'options' to be array param")
	}
	if IsArrayParam("file_path") {
		t.Error("expected 'file_path' to NOT be array param")
	}
}

func TestFixStructure_WrapScalar(t *testing.T) {
	input := map[string]interface{}{"questions": "single question"}
	got := FixStructure(input)
	arr, ok := got["questions"].([]interface{})
	if !ok || len(arr) != 1 || arr[0] != "single question" {
		t.Errorf("expected ['single question'], got %v", got["questions"])
	}
}

func TestFixStructure_ParseJSONObject(t *testing.T) {
	input := map[string]interface{}{"config": `{"key": "val"}`}
	got := FixStructure(input)
	m, ok := got["config"].(map[string]interface{})
	if !ok || m["key"] != "val" {
		t.Errorf("expected map[key:val], got %v", got["config"])
	}
}

func TestFixStructure_ParseJSONArray(t *testing.T) {
	input := map[string]interface{}{"items": `[1, 2, 3]`}
	got := FixStructure(input)
	arr, ok := got["items"].([]interface{})
	if !ok || len(arr) != 3 {
		t.Errorf("expected [1,2,3], got %v", got["items"])
	}
}

func TestFixStructure_Nil(t *testing.T) {
	if got := FixStructure(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestGetDefaultValue(t *testing.T) {
	val, ok := GetDefaultValue("Bash", "timeout")
	if !ok {
		t.Fatal("expected Bash.timeout to have default")
	}
	if val.(float64) != 30000 {
		t.Errorf("expected 30000, got %v", val)
	}
	_, ok = GetDefaultValue("Bash", "nonexistent")
	if ok {
		t.Error("expected nonexistent param to not have default")
	}
	_, ok = GetDefaultValue("NonexistentTool", "param")
	if ok {
		t.Error("expected nonexistent tool to not have defaults")
	}
}

func TestApplyDefaults(t *testing.T) {
	input := map[string]interface{}{"command": "ls"}
	got := ApplyDefaults("Bash", input)
	if got["command"] != "ls" {
		t.Errorf("expected command='ls', got %v", got["command"])
	}
	if got["timeout"].(float64) != 30000 {
		t.Errorf("expected timeout=30000, got %v", got["timeout"])
	}
}

func TestApplyDefaults_ExistingValue(t *testing.T) {
	input := map[string]interface{}{"timeout": float64(5000)}
	got := ApplyDefaults("Bash", input)
	if got["timeout"].(float64) != 5000 {
		t.Errorf("expected existing timeout=5000 preserved, got %v", got["timeout"])
	}
}

func TestFixParameters(t *testing.T) {
	input := map[string]interface{}{
		"cmd":   "ls -la",
		"path":  "/tmp",
		"level": "3",
	}
	got, summary := FixParameters("Bash", input, "")
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got["command"] != "ls -la" {
		t.Errorf("expected command='ls -la', got %v", got["command"])
	}
	if _, exists := got["cmd"]; exists {
		t.Error("cmd alias should have been removed")
	}
	if got["file_path"] != "/tmp" {
		t.Errorf("expected file_path='/tmp', got %v", got["file_path"])
	}
	if got["timeout"].(float64) != 30000 {
		t.Errorf("expected timeout=30000, got %v", got["timeout"])
	}
	if got["level"].(float64) != 3 {
		t.Errorf("expected level=3, got %v", got["level"])
	}
	if summary.Total() == 0 {
		t.Error("expected corrections to be detected")
	}
}

func TestFixParameters_Nil(t *testing.T) {
	got, summary := FixParameters("Bash", nil, "")
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	if summary.Total() != 0 {
		t.Errorf("expected 0 corrections, got %d", summary.Total())
	}
}

func TestFixParameters_FixSummary_TypeCoercions(t *testing.T) {
	input := map[string]interface{}{
		"command": "ls",
		"timeout": "30", // string that should be number
	}
	_, summary := FixParameters("Bash", input, "")
	if summary.TypeCoercions == 0 {
		t.Error("expected type_coercions to be > 0")
	}
}

func TestFixParameters_FixSummary_NameMappings(t *testing.T) {
	input := map[string]interface{}{
		"cmd":   "ls -la",
		"path":  "/tmp",
	}
	_, summary := FixParameters("Bash", input, "")
	if summary.NameMappings == 0 {
		t.Error("expected name_mappings to be > 0")
	}
}

func TestFixParameters_FixSummary_Defaults(t *testing.T) {
	input := map[string]interface{}{
		"command": "ls",
		// timeout is missing, should get default
	}
	_, summary := FixParameters("Bash", input, "")
	if summary.Defaults == 0 {
		t.Error("expected defaults to be > 0")
	}
}

func TestFixParameters_FixSummary_StructureFixes(t *testing.T) {
	input := map[string]interface{}{
		"command": "ls",
		"questions": "single question", // should be wrapped in array
	}
	_, summary := FixParameters("Bash", input, "")
	if summary.StructureFixes == 0 {
		t.Error("expected structure_fixes to be > 0")
	}
}

func TestFixParameters_FixSummary_NoCorrections(t *testing.T) {
	input := map[string]interface{}{
		"command": "ls",
		"timeout": float64(30000),
	}
	_, summary := FixParameters("Bash", input, "")
	if summary.Total() != 0 {
		t.Errorf("expected 0 corrections, got %d", summary.Total())
	}
}
