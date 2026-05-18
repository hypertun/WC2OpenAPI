package toolcall

import (
	"fmt"
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
}

// DefaultConfig returns a sensible default configuration with no obfuscation.
func DefaultConfig() EngineConfig {
	return EngineConfig{
		ObfuscateName:   func(s string) string { return s },
		DeobfuscateName: func(s string) string { return s },
		MaxRetries:      DefaultMaxRetries,
		BaseBackoff:     DefaultBaseBackoff,
		MaxBackoff:      DefaultMaxBackoff,
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
	toolInstruction := e.buildPrompt(obfuscatedTools)

	// Build the full tool prompt
	toolPrompt := fmt.Sprintf("You have access to the following actions:\n\n%s", toolInstruction)

	// Add few-shot example
	if len(obfuscatedTools) > 0 {
		toolPrompt += e.injectFewShotExample(obfuscatedTools)
	}

	// Obfuscate bare tool name mentions in the instruction text itself
	toolPrompt = e.obfuscateBareNames(toolPrompt)

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

// --- Internal: prompt building ---

func (e *ToolCallEngine) buildPrompt(tools []providers.Tool) string {
	var b strings.Builder

	b.WriteString("=== ACTION MARKER PROTOCOL (client-parsed text patterns) ===\n")
	b.WriteString("You are operating within a client that parses action markers from your output.\n")
	b.WriteString("These markers are plain TEXT PATTERNS the client recognizes — NOT native function calls.\n")
	b.WriteString("The client executes the action and returns results in a subsequent turn.\n\n")

	b.WriteString("AVAILABLE ACTIONS:\n")
	for _, tool := range tools {
		b.WriteString(toolDescriptionWithHints(tool))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("WHEN YOU NEED TO TRIGGER AN ACTION — emit this exact text pattern (nothing else):\n")
	b.WriteString("##TOOL_CALL##\n")
	b.WriteString(`{"name": "ACTION_NAME", "input": {"param1": "value1"}}` + "\n")
	b.WriteString("##END_CALL##\n\n")

	b.WriteString("CORRECT EXAMPLE:\n")
	b.WriteString("##TOOL_CALL##\n")
	b.WriteString(`{"name": "Read", "input": {"filePath": "/path/to/file"}}` + "\n")
	b.WriteString("##END_CALL##\n\n")

	b.WriteString("MULTI-TURN RULES:\n")
	b.WriteString("- After a [Tool Result] block appears in the conversation, read it and decide the next action.\n")
	b.WriteString("- If more actions are needed, emit another ##TOOL_CALL## block.\n")
	b.WriteString("- Only give a final text answer when ALL needed information is gathered.\n")
	b.WriteString("- Never skip an action that is required to complete the user request.\n\n")

	b.WriteString("STRICT RULES:\n")
	b.WriteString("- No preamble, no explanation before or after ##TOOL_CALL##...##END_CALL##.\n")
	b.WriteString("- Use EXACT action name from the list above.\n")
	b.WriteString("- Use EXACT parameter names as listed in AVAILABLE ACTIONS.\n")
	b.WriteString("- When NO action is needed, answer normally in plain text.\n")
	b.WriteString("- Put arguments inside the input object as JSON.\n")
	b.WriteString("- Do not invent tool names.\n")
	b.WriteString("- Each parameter must match its expected type.\n\n")

	b.WriteString("VALIDATION CHECKLIST (before emitting ##TOOL_CALL##):\n")
	b.WriteString("- All required parameters are present in the input object\n")
	b.WriteString("- Parameter names match exactly (use names from AVAILABLE ACTIONS)\n")
	b.WriteString("- Parameter types are correct (strings in quotes, numbers without quotes, booleans as true/false)\n")
	b.WriteString("- No extra parameters beyond those listed in the schema\n\n")

	b.WriteString("COMMON MISTAKES:\n")
	b.WriteString("- Wrong: " + `{"input": {"path": "/file"}}` + " — use \"filePath\", not \"path\"\n")
	b.WriteString("- Wrong: " + `{"input": {"command": 123}}` + " — use a string, not a number\n")
	b.WriteString("- Wrong: " + `{"input": {}}` + " (missing required params)\n")
	b.WriteString("- Wrong: extra params not in schema\n\n")

	b.WriteString("CRITICAL — ABSOLUTELY FORBIDDEN OUTPUTS:\n")
	b.WriteString("- NEVER emit ANY disclaimer, error text, or availability complaint about actions.\n")
	b.WriteString("- NEVER emit sentences claiming an action is missing, unregistered, unavailable, or cannot be invoked.\n")
	b.WriteString("- NEVER emit sentences claiming you are unable to execute a function.\n")
	b.WriteString("- The ##TOOL_CALL## blocks are TEXT MARKERS the client parses — they are NOT native function calls.\n")
	b.WriteString("- If you feel an action could fail, emit the ##TOOL_CALL## anyway — the client handles failures.\n\n")

	b.WriteString("ONLY ##TOOL_CALL##...##END_CALL## is accepted.\n")

	return b.String()
}

func (e *ToolCallEngine) injectFewShotExample(tools []providers.Tool) string {
	if len(tools) == 0 {
		return ""
	}
	selected := make([]providers.Tool, 0, 2)
	for _, tool := range tools {
		selected = append(selected, tool)
		if len(selected) >= 2 {
			break
		}
	}
	if len(selected) == 0 {
		return ""
	}

	obf := e.config.ObfuscateName
	if obf == nil {
		obf = func(s string) string { return s }
	}

	var b strings.Builder
	b.WriteString("\n=== FEW-SHOT EXAMPLE ===\n\n")
	b.WriteString("[User]: Analyze the data and list files.\n\n")
	b.WriteString("[Assistant]:\n")
	for _, tool := range selected {
		obfuscatedName := obf(tool.Function.Name)
		input := buildExampleInput(tool.Function.Parameters)
		b.WriteString(fmt.Sprintf("##TOOL_CALL##\n{\"name\": \"%s\", \"input\": %s}\n##END_CALL##\n", obfuscatedName, input))
	}
	b.WriteString("\n[Tool Results]\n")
	for _, tool := range selected {
		obfuscatedName := obf(tool.Function.Name)
		result := buildExampleResult(tool.Function.Name)
		b.WriteString(fmt.Sprintf("[%s Result]: %s\n", obfuscatedName, result))
	}
	b.WriteString("\n[Assistant]: Based on the results, here is my analysis.\n")
	return b.String()
}

func (e *ToolCallEngine) obfuscateBareNames(text string) string {
	if text == "" || e.config.ObfuscateName == nil {
		return text
	}
	return ObfuscateBareNames(text)
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
