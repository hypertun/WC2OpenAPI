package toolcall

import (
	"math"
	"math/rand"
	"time"

	providers "github.com/user/wc2api/internal/providers"
)

const (
	DefaultMaxRetries    = 3
	DefaultBaseBackoff   = 100 * time.Millisecond
	DefaultMaxBackoff    = 2 * time.Second
	backoffJitterFraction = 0.25
)

func ShouldRetry(validationErrors []*ValidationError, retryCount, maxRetries int) bool {
	if retryCount < 0 {
		return false
	}
	return len(validationErrors) > 0 && retryCount < maxRetries
}

func CalculateBackoff(retryCount int) time.Duration {
	if retryCount <= 0 {
		return 0
	}
	backoff := float64(DefaultBaseBackoff) * math.Pow(2, float64(retryCount-1))
	if backoff > float64(DefaultMaxBackoff) {
		backoff = float64(DefaultMaxBackoff)
	}
	jitter := backoff * backoffJitterFraction * (rand.Float64()*2 - 1)
	return time.Duration(backoff + jitter)
}

func BuildRetryRequest(original *providers.ChatRequest, feedback string) *providers.ChatRequest {
	newMessages := make([]providers.Message, len(original.Messages)+1)
	newMessages[0] = providers.Message{
		Role:    "system",
		Content: providers.MessageContent(feedback),
	}
	copy(newMessages[1:], original.Messages)

	return &providers.ChatRequest{
		Model:       original.Model,
		Messages:    newMessages,
		Tools:       original.Tools,
		ToolChoice:  original.ToolChoice,
		Temperature: original.Temperature,
		MaxTokens:   original.MaxTokens,
		Stream:      original.Stream,
	}
}
