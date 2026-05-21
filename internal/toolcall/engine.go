package toolcall

import (
	"math"
	"math/rand"
	"regexp"
	"strings"
	"time"

	providers "github.com/user/wc2api/internal/providers"
)

// EngineConfig configures the unified ToolCallEngine.
type EngineConfig struct {
	// ObfuscateName obfuscates tool names before sending to the provider.
	// Default: identity (no obfuscation).
	ObfuscateName func(string) string
	// DeobfuscateName reverses obfuscation on provider responses.
	// Default: identity (no deobfuscation).
	DeobfuscateName func(string) string
	// MaxRetries is the maximum number of retry attempts on validation errors.
	// Default: 3.
	MaxRetries int
	// BaseBackoff is the base duration for exponential backoff between retries.
	// Default: 100ms.
	BaseBackoff time.Duration
	// MaxBackoff is the maximum backoff duration.
	// Default: 2s.
	MaxBackoff time.Duration
	// Compact enables compact prompt mode: shorter boilerplate, first-sentence-only descriptions.
	// Default: false.
	Compact bool
}

// DefaultConfig returns a sensible default configuration with no obfuscation.
func DefaultConfig() EngineConfig {
	return EngineConfig{
		ObfuscateName:   func(s string) string { return s },
		DeobfuscateName: func(s string) string { return s },
		MaxRetries:      DefaultMaxRetries,
		BaseBackoff:     DefaultBaseBackoff,
		MaxBackoff:      DefaultMaxBackoff,
		Compact:         false,
	}
}

// CompactConfig returns a configuration optimized for small-context providers.
// It uses the compact prompt format to reduce token overhead.
func CompactConfig() EngineConfig {
	return EngineConfig{
		ObfuscateName:   func(s string) string { return s },
		DeobfuscateName: func(s string) string { return s },
		MaxRetries:      DefaultMaxRetries,
		BaseBackoff:     DefaultBaseBackoff,
		MaxBackoff:      DefaultMaxBackoff,
		Compact:         true,
	}
}

// QwenConfig returns the recommended config for the Qwen provider.
// It enables tool name obfuscation to avoid Qwen's built-in namespace collisions.
func QwenConfig() EngineConfig {
	return EngineConfig{
		ObfuscateName:   ObfuscateToolName,
		DeobfuscateName: DeobfuscateToolName,
		MaxRetries:      DefaultMaxRetries,
		BaseBackoff:     DefaultBaseBackoff,
		MaxBackoff:      DefaultMaxBackoff,
		Compact:         false,
	}
}

// QwenCompactConfig returns the recommended config for the Qwen provider with compact mode.
// It enables tool name obfuscation AND compact prompt format to reduce token overhead.
func QwenCompactConfig() EngineConfig {
	return EngineConfig{
		ObfuscateName:   ObfuscateToolName,
		DeobfuscateName: DeobfuscateToolName,
		MaxRetries:      DefaultMaxRetries,
		BaseBackoff:     DefaultBaseBackoff,
		MaxBackoff:      DefaultMaxBackoff,
		Compact:         true,
	}
}

// ToolCallEngine is the unified interface for tool calling across all providers.
// It combines prompt injection, parsing, validation, retry, and error feedback
// into a single layer so that Qwen, MiMo, QwenCN, and future providers all
// share the same robust tool-call pipeline.
type ToolCallEngine struct {
	config EngineConfig
}

// New creates a ToolCallEngine with the given configuration.
func New(config EngineConfig) *ToolCallEngine {
	return &ToolCallEngine{config: config}
}

// InjectTools injects tool call instructions into the message list.
// It obfuscates tool names, builds the rich prompt (with checklist, negative
// examples, etc.), handles tool_choice policy, and appends a few-shot example.
func (e *ToolCallEngine) InjectTools(
	messages []providers.Message,
	tools []providers.Tool,
	toolChoice providers.ToolChoice,
) []providers.Message {
	if len(tools) == 0 {
		return messages
	}

	obf := e.config.ObfuscateName
	if obf == nil {
		obf = func(s string) string { return s }
	}

	// Create obfuscated copy of tools
	obfuscatedTools := make([]providers.Tool, len(tools))
	for i, tool := range tools {
		obfuscatedTools[i] = tool
		obfuscatedTools[i].Function.Name = obf(tool.Function.Name)
	}

	// Build tool call instructions
	var toolPrompt string
	if e.config.Compact {
		toolPrompt = BuildCompactMarkerPrompt(obfuscatedTools)
	} else {
		toolPrompt = BuildMarkerPrompt(obfuscatedTools, false)
		// Add few-shot example only in non-compact mode
		if len(obfuscatedTools) > 0 {
			toolPrompt += injectFewShotExample(obfuscatedTools, e.config.ObfuscateName)
		}
	}

	// Obfuscate bare tool name mentions in the instruction text itself
	if e.config.ObfuscateName != nil {
		toolPrompt = ObfuscateBareNames(toolPrompt)
	}

	// Handle tool_choice policy
	switch tc := toolChoice.(type) {
	case string:
		if tc == "none" {
			return messages
		}
		if tc == "required" {
			toolPrompt += "\n\nIMPORTANT: You MUST call one of the available tools. Do not respond without calling a tool."
		}
	}

	// Inject tool prompt into system message or create new one
	result := make([]providers.Message, len(messages))
	toolPromptInjected := false
	for i, msg := range messages {
		if msg.Role == "system" && !toolPromptInjected {
			result[i] = providers.Message{
				Role:    "system",
				Content: providers.MessageContent(string(msg.Content) + "\n\n" + toolPrompt),
			}
			toolPromptInjected = true
		} else {
			result[i] = msg
		}
	}
	if !toolPromptInjected {
		result = append([]providers.Message{{Role: "system", Content: providers.MessageContent(toolPrompt)}}, result...)
	}

	return result
}

// Parse extracts tool calls from provider response text using multiple strategies:
//  1. ##TOOL_CALL##...##END_CALL## markers (primary)
//  2. Normalized text (fix typos, fragmented markers)
//  3. MiMoML / DSML XML with enhanced noise tolerance
//  4. XML wrapper / singular formats (existing)
//  5. TOOL_CALL: name(args) pattern
//  6. Bare JSON {"name":"x","arguments":{...}}
//  7. <function_call> JSON+XML
//
// It also de-obfuscates tool names, applies tool-specific parameter fixes,
// coerces types, and repairs malformed JSON.
func (e *ToolCallEngine) Parse(
	text string,
	tools []providers.Tool,
) ([]providers.ToolCall, string, []*ValidationError) {
	if text == "" {
		return nil, text, nil
	}

	toolNames := getToolNames(tools)
	deobf := e.config.DeobfuscateName
	if deobf == nil {
		deobf = func(s string) string { return s }
	}

	// Strategy 1: ##TOOL_CALL## markers
	if strings.Contains(text, "##TOOL_CALL##") || strings.Contains(text, "##END_CALL##") {
		calls, errs := ParseToolCallsFromText(text, tools)
		if len(calls) > 0 {
			for i := range calls {
				calls[i].Function.Name = deobf(calls[i].Function.Name)
			}
			cleaned := e.cleanMarkerText(text)
			return calls, cleaned, errs
		}
	}

	// Strategy 2: Normalized text
	normalized := NormalizeFragmentedToolCall(text)
	if strings.Contains(normalized, "##TOOL_CALL##") {
		calls, errs := ParseToolCallsFromText(normalized, tools)
		if len(calls) > 0 {
			for i := range calls {
				calls[i].Function.Name = deobf(calls[i].Function.Name)
			}
			cleaned := e.cleanMarkerText(text)
			return calls, cleaned, errs
		}
	}

	// Strategy 3: XML / DSML formats (MiMo)
	calls, cleaned := ParseXMLToolCalls(text, toolNames)
	if len(calls) > 0 {
		return calls, cleaned, nil
	}

	// Strategy 4-7: New extraction strategies via ParseAllToolCalls
	sieveCalls, sieveCleaned := ParseAllToolCalls(text, toolNames)
	if len(sieveCalls) > 0 {
		provCalls := make([]providers.ToolCall, len(sieveCalls))
		for i, sc := range sieveCalls {
			provCalls[i] = providers.ToolCall{
				ID:   sc.ID,
				Type: sc.Type,
				Function: providers.ToolCallFunction{
					Name:      deobf(sc.Function.Name),
					Arguments: sc.Function.Arguments,
				},
			}
		}
		return provCalls, sieveCleaned, nil
	}

	// Check for residual markers
	if strings.Contains(text, "##TOOL_CALL##") || strings.Contains(text, "##END_CALL##") {
		return nil, e.cleanMarkerText(text), nil
	}

	return nil, text, nil
}

// Validate validates parsed tool calls against the tool definitions.
func (e *ToolCallEngine) Validate(calls []providers.ToolCall, tools []providers.Tool) []*ValidationError {
	return ValidateToolCallsWithErrors(calls, tools)
}

// ShouldRetry returns true if the retry loop should continue.
func (e *ToolCallEngine) ShouldRetry(validationErrors []*ValidationError, retryCount int) bool {
	if retryCount < 0 {
		return false
	}
	maxRetries := e.config.MaxRetries
	if maxRetries == 0 {
		maxRetries = DefaultMaxRetries
	}
	return len(validationErrors) > 0 && retryCount < maxRetries
}

// CalculateBackoff returns the backoff duration for the given retry count.
func (e *ToolCallEngine) CalculateBackoff(retryCount int) time.Duration {
	if retryCount <= 0 {
		return 0
	}
	base := float64(e.config.BaseBackoff)
	if base == 0 {
		base = float64(DefaultBaseBackoff)
	}
	max := float64(e.config.MaxBackoff)
	if max == 0 {
		max = float64(DefaultMaxBackoff)
	}
	backoff := base * math.Pow(2, float64(retryCount-1))
	if backoff > max {
		backoff = max
	}
	jitter := backoff * backoffJitterFraction * (rand.Float64()*2 - 1)
	return time.Duration(backoff + jitter)
}

// BuildRetryRequest builds a new ChatRequest with error feedback prepended.
func (e *ToolCallEngine) BuildRetryRequest(original *providers.ChatRequest, feedback string) *providers.ChatRequest {
	return BuildRetryRequest(original, feedback)
}

// GenerateErrorFeedback generates human-readable error feedback from validation errors.
func (e *ToolCallEngine) GenerateErrorFeedback(errors []*ValidationError) string {
	return GenerateToolCallErrorFeedback(errors)
}

// ObfuscateName applies the engine's configured tool name obfuscation.
// This is used by providers to re-obfuscate tool names in conversation history
// before sending to the model.
func (e *ToolCallEngine) ObfuscateName(name string) string {
	return e.config.ObfuscateName(name)
}

func (e *ToolCallEngine) cleanMarkerText(text string) string {
	if text == "" {
		return text
	}
	re := regexp.MustCompile(`(?s)##TOOL_CALL##\s*.+?\s*##END_CALL##`)
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile(`\n{3,}`)
	text = re.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// getToolNames extracts function names from the tools slice.
func getToolNames(tools []providers.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t.Type == "function" && t.Function.Name != "" {
			names = append(names, t.Function.Name)
		}
	}
	return names
}
