package toolcall

import "fmt"

const maxFeedbackErrors = 10

func GenerateToolCallErrorFeedback(errors []*ValidationError) string {
	if len(errors) == 0 {
		return ""
	}

	toolErrors := make(map[string][]string)
	totalErrors := 0
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
		totalErrors++
	}

	// Truncate if too many errors
	truncated := false
	if totalErrors > maxFeedbackErrors {
		truncated = true
		// Flatten and truncate
		var flatErrs []string
		for _, errs := range toolErrors {
			flatErrs = append(flatErrs, errs...)
		}
		if len(flatErrs) > maxFeedbackErrors {
			flatErrs = flatErrs[:maxFeedbackErrors]
		}
		// Rebuild toolErrors from flatErrs (simplified - just put all in "errors" key)
		toolErrors = map[string][]string{"errors": flatErrs}
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
	if truncated {
		msg += fmt.Sprintf("... and %d more errors (truncated for brevity)\n\n", totalErrors-maxFeedbackErrors)
	}
	msg += "Please retry with corrected parameters. " +
		"Ensure all required fields are provided with correct types."

	return msg
}
