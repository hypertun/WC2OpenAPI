# Tool Call Error Fix Implementation Plan

## Overview
Fixing tool call parameter validation errors and implementing error correction for WC2OpenAPI.

## Phase 1: Enhanced Parameter Validation

### Step 1.1: Create Parameter Validation Module
- [x] Create `internal/toolcall/validator.go`
- [x] Implement JSON schema validation for tool parameters
- [x] Add `ValidateToolCall` function to check:
  - [x] Required fields presence
  - [x] Parameter type compliance
  - [x] Format validation (strings, numbers, etc.)
- [x] Add unit tests for validator

### Step 1.2: Integrate Validation into Parsing Pipeline
- [x] Update `internal/providers/qwen/toolparse.go:parseToolCallsFromText()` 
- [x] Update `internal/providers/deepseek/toolparse.go:parseToolCallsFromText()`
- [x] Add validation step before returning parsed tool calls
- [x] Log validation errors with details

## Phase 2: Automatic Parameter Correction

### Step 2.1: Enhance Parameter Fixing Logic
- [x] Extend `internal/providers/qwen/toolparse.go:fixToolCallArguments()`
- [x] Add type coercion for compatible types (string↔number, boolean variations)
- [x] Add default value insertion for missing non-required fields
- [x] Add parameter name mapping for common variations
- [x] Add array/object structure corrections

### Step 2.2: Create General Parameter Fixing Rules
- [x] Create `internal/toolcall/param_fixer.go`
- [x] Implement common parameter name mappings (path/file_path, cmd/command, etc.)
- [x] Add type conversion helpers
- [x] Add default value definitions for common tool parameters
- [x] Add unit tests for param fixer

## Phase 3: Error Feedback System

### Step 3.1: Create Error Message Generator
- [ ] Create `internal/toolcall/error_feedback.go`
- [ ] Implement `GenerateToolCallErrorFeedback` function
- [ ] Create specific error messages for:
  - Missing required parameters
  - Invalid parameter types
  - Unrecognized tool names
  - Malformed tool call structure
- [ ] Include corrective examples in error messages

### Step 3.2: Integrate Error Feedback into Chat Pipeline
- [ ] Update `internal/providers/qwen/chat.go:CreateChatCompletionStream()`
- [ ] Update `internal/providers/deepseek/chat.go:CreateChatCompletionStream()`
- [ ] Add retry logic with error feedback
- [ ] Add system message injection for error feedback
- [ ] Limit retry attempts (max 3 retries)

## Phase 4: Improved Prompt Engineering

### Step 4.1: Enhance Tool Call Prompts
- [ ] Update `internal/toolcall/tool_prompt.go:BuildToolCallInstructions()`
- [ ] Add explicit parameter type information
- [ ] Include examples of correctly formatted tool calls
- [ ] Add common warnings about parameter mistakes
- [ ] Strengthen parameter validation guidance in "STRICT RULES"

### Step 4.2: Create Tool-Specific Prompt Enhancements
- [ ] Update `internal/toolcall/tool_prompt.go:BuildQwenToolCallInstructions()`
- [ ] Add parameter type examples for each common tool
- [ ] Include negative examples (what NOT to do)
- [ ] Add parameter validation checklist in prompt

## Phase 5: Retry Logic Implementation

### Step 5.1: Add Retry Detection Logic
- [ ] Create `internal/toolcall/retry.go`
- [ ] Implement `ShouldRetryToolCall` function
- [ ] Add retry counter and error tracking
- [ ] Define retry conditions (parameter errors only)

### Step 5.2: Implement Retry Flow
- [ ] Add retry wrapper in both providers' chat handlers
- [ ] Implement exponential backoff for retries
- [ ] Add fallback to text response after max retries
- [ ] Add detailed logging for retry attempts

## Phase 6: Enhanced Logging

### Step 6.1: Add Detailed Tool Call Logging
- [ ] Update `internal/providers/qwen/toolparse.go`
- [ ] Update `internal/providers/deepseek/toolparse.go`
- [ ] Log original tool call structure
- [ ] Log parameter validation results
- [ ] Log automatic corrections applied
- [ ] Log retry attempts and outcomes

### Step 6.2: Add Structured Logging
- [ ] Use structured logging with slog
- [ ] Add log levels for different tool call events
- [ ] Include correlation IDs for tracking retries
- [ ] Add performance metrics logging

## Phase 7: Testing and Validation

### Step 7.1: Create Comprehensive Test Suite
- [ ] Add unit tests for validator in `internal/toolcall/validator_test.go`
- [ ] Add unit tests for param fixer in `internal/toolcall/param_fixer_test.go`
- [ ] Add unit tests for error feedback in `internal/toolcall/error_feedback_test.go`
- [ ] Add integration tests for retry logic

### Step 7.2: Test with Real Tool Calls
- [ ] Create test cases for various parameter errors
- [ ] Test automatic corrections with malformed tool calls
- [ ] Test error feedback loop with models
- [ ] Verify retry limits and fallback behavior

## Phase 8: Documentation

### Step 8.1: Update Documentation
- [ ] Update tool call behavior documentation
- [ ] Add examples of error correction
- [ ] Document retry behavior in API docs
- [ ] Add troubleshooting guide for tool call errors

### Step 8.2: Create Migration Guide
- [ ] Document breaking changes if any
- [ ] Create configuration options for new behavior
- [ ] Add migration checklist for existing deployments

## Success Criteria

- [ ] Tool calls with wrong parameter types are automatically fixed
- [ ] Tool calls with missing required params trigger error feedback
- [ ] Models retry with corrected parameters after error feedback
- [ ] Invalid tool names are reported clearly to models
- [ ] Retry logic prevents infinite loops
- [ ] All tool call interactions are properly logged
- [ ] Test coverage > 90% for new code
- [ ] Documentation is updated and comprehensive

## Rollback Plan

If issues occur:
1. Disable automatic parameter fixing via config
2. Disable retries via config
3. Revert prompt changes to original version
4. Keep logging for debugging

## Notes
- Focus on the most common parameter errors first
- Keep retry attempts limited to prevent resource waste
- Log everything to help with future debugging
- Test with both Qwen and DeepSeek providers