package toolcall

import (
	"testing"
	"time"

	providers "github.com/user/wc2api/internal/providers"
)

func TestShouldRetry_NoErrors(t *testing.T) {
	if ShouldRetry(nil, 0, 3) {
		t.Error("ShouldRetry with nil errors should return false")
	}
	if ShouldRetry([]*ValidationError{}, 0, 3) {
		t.Error("ShouldRetry with empty errors should return false")
	}
}

func TestShouldRetry_MaxRetriesExceeded(t *testing.T) {
	errors := []*ValidationError{
		{Message: "test error"},
	}
	if ShouldRetry(errors, 3, 3) {
		t.Error("ShouldRetry when retryCount >= maxRetries should return false")
	}
	if ShouldRetry(errors, 5, 3) {
		t.Error("ShouldRetry when retryCount >> maxRetries should return false")
	}
}

func TestShouldRetry_ShouldRetry(t *testing.T) {
	errors := []*ValidationError{
		{Message: "test error"},
	}
	if !ShouldRetry(errors, 0, 3) {
		t.Error("ShouldRetry with errors and count=0 should return true")
	}
	if !ShouldRetry(errors, 1, 3) {
		t.Error("ShouldRetry with errors and count=1 should return true")
	}
	if !ShouldRetry(errors, 2, 3) {
		t.Error("ShouldRetry with errors and count=2 should return true")
	}
}

func TestCalculateBackoff_Zero(t *testing.T) {
	d := CalculateBackoff(0)
	if d != 0 {
		t.Errorf("CalculateBackoff(0) = %v, want 0", d)
	}
}

func TestCalculateBackoff_Negative(t *testing.T) {
	d := CalculateBackoff(-1)
	if d != 0 {
		t.Errorf("CalculateBackoff(-1) = %v, want 0", d)
	}
}

func TestCalculateBackoff_Retry1(t *testing.T) {
	d := CalculateBackoff(1)
	if d < 50*time.Millisecond || d > 200*time.Millisecond {
		t.Errorf("CalculateBackoff(1) = %v, want ~100ms ±50%%", d)
	}
}

func TestCalculateBackoff_Retry2(t *testing.T) {
	d := CalculateBackoff(2)
	if d < 100*time.Millisecond || d > 400*time.Millisecond {
		t.Errorf("CalculateBackoff(2) = %v, want ~200ms ±50%%", d)
	}
}

func TestCalculateBackoff_Retry3(t *testing.T) {
	d := CalculateBackoff(3)
	if d < 200*time.Millisecond || d > 800*time.Millisecond {
		t.Errorf("CalculateBackoff(3) = %v, want ~400ms ±50%%", d)
	}
}

func TestCalculateBackoff_Capped(t *testing.T) {
	for i := 4; i <= 10; i++ {
		d := CalculateBackoff(i)
		if d > DefaultMaxBackoff*2 {
			t.Errorf("CalculateBackoff(%d) = %v, exceeded max backoff (2s) by too much", i, d)
		}
	}
}

func TestCalculateBackoff_Monotonic(t *testing.T) {
	var prev time.Duration
	for i := 1; i <= 5; i++ {
		d := CalculateBackoff(i)
		if i > 1 && d < prev*3/5 {
			t.Errorf("CalculateBackoff(%d) = %v, should be roughly >= previous (%v)", i, d, prev)
		}
		prev = d
	}
}

func TestBuildRetryRequest(t *testing.T) {
	original := &providers.ChatRequest{
		Model:       "test-model",
		Messages:    []providers.Message{{Role: "user", Content: "hello"}},
		Tools:       nil,
		ToolChoice:  "auto",
		Temperature: 0.7,
		MaxTokens:   1000,
		Stream:      false,
	}

	feedback := "fix your errors!"
	retryReq := BuildRetryRequest(original, feedback)

	if retryReq.Model != original.Model {
		t.Errorf("Model = %q, want %q", retryReq.Model, original.Model)
	}
	if retryReq.Temperature != original.Temperature {
		t.Errorf("Temperature = %v, want %v", retryReq.Temperature, original.Temperature)
	}
	if retryReq.MaxTokens != original.MaxTokens {
		t.Errorf("MaxTokens = %d, want %d", retryReq.MaxTokens, original.MaxTokens)
	}
	if retryReq.Stream != original.Stream {
		t.Errorf("Stream = %v, want %v", retryReq.Stream, original.Stream)
	}

	if len(retryReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(retryReq.Messages))
	}
	if retryReq.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want 'system'", retryReq.Messages[0].Role)
	}
	if string(retryReq.Messages[0].Content) != feedback {
		t.Errorf("first message content = %q, want %q", string(retryReq.Messages[0].Content), feedback)
	}
	if string(retryReq.Messages[1].Content) != "hello" {
		t.Errorf("second message content = %q, want 'hello'", string(retryReq.Messages[1].Content))
	}
}

func TestBuildRetryRequest_WithStream(t *testing.T) {
	original := &providers.ChatRequest{
		Model:  "qwen3.5-flash",
		Stream: true,
	}
	retryReq := BuildRetryRequest(original, "fix it")
	if retryReq.Stream != true {
		t.Error("Stream should be preserved")
	}
}

func TestBuildRetryRequest_PreservesTools(t *testing.T) {
	tools := []providers.Tool{
		{Type: "function", Function: providers.ToolFunction{Name: "Read"}},
	}
	original := &providers.ChatRequest{
		Model:  "test",
		Tools:  tools,
		Stream: false,
	}
	retryReq := BuildRetryRequest(original, "fix it")
	if len(retryReq.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(retryReq.Tools))
	}
	if retryReq.Tools[0].Function.Name != "Read" {
		t.Errorf("Tool name = %q, want 'Read'", retryReq.Tools[0].Function.Name)
	}
}

func TestCalculateBackoff_JitterWithinRange(t *testing.T) {
	// Jitter is ±25% of base backoff
	// For retry=1: base=100ms, jitter should be 75-125ms
	d := CalculateBackoff(1)
	if d < 75*time.Millisecond || d > 125*time.Millisecond {
		t.Errorf("CalculateBackoff(1) = %v, want 75-125ms", d)
	}
}

func TestCalculateBackoff_JitterMultipleSamples(t *testing.T) {
	// Run multiple samples and verify they're not all the same (jitter varies)
	values := make(map[time.Duration]bool)
	for i := 0; i < 100; i++ {
		d := CalculateBackoff(1)
		values[d] = true
	}
	// Should have multiple different values due to jitter
	if len(values) < 2 {
		t.Errorf("Expected jitter to produce multiple values, got %d unique values", len(values))
	}
}

func TestShouldRetry_ZeroMaxRetries(t *testing.T) {
	errors := []*ValidationError{{Message: "test"}}
	if ShouldRetry(errors, 0, 0) {
		t.Error("ShouldRetry with maxRetries=0 should return false")
	}
}

func TestShouldRetry_NegativeRetryCount(t *testing.T) {
	errors := []*ValidationError{{Message: "test"}}
	if ShouldRetry(errors, -1, 3) {
		t.Error("ShouldRetry with negative retry count should return false")
	}
}
