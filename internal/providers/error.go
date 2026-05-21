package providers

import "strings"

// tooLongPatterns contains substrings that indicate a provider rejected the input
// as too long. Each pattern is checked case-insensitively.
var tooLongPatterns = []string{
	"the text you sent is too long",
	"simplify the content appropriately",
	"send it in parts",
}

// IsTooLongError checks whether the response content matches known
// "input too long" error messages from providers like MiMo.
func IsTooLongError(content string) bool {
	lower := strings.ToLower(content)
	for _, pat := range tooLongPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}
