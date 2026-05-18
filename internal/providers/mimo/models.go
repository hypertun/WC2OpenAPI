package mimo

import (
	"context"
	"time"

	providers "github.com/user/wc2api/internal/providers"
)

// ListModels returns available models discovered from the MiMo API.
// Falls back to a minimal default list if discovery fails.
func (c *Client) ListModels() []providers.Model {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	models, err := c.getCachedModels(ctx)
	if err != nil || len(models) == 0 {
		// Fallback to known models if discovery fails
		return []providers.Model{
			{ID: "mimo-v2-pro", Object: "model", Created: time.Now().Unix(), OwnedBy: "xiaomi"},
			{ID: "mimo-v2-flash", Object: "model", Created: time.Now().Unix(), OwnedBy: "xiaomi"},
			{ID: "mimo-v2-omni", Object: "model", Created: time.Now().Unix(), OwnedBy: "xiaomi"},
		}
	}

	result := make([]providers.Model, 0, len(models))
	now := time.Now().Unix()
	for _, m := range models {
		result = append(result, providers.Model{
			ID:      m,
			Object:  "model",
			Created: now,
			OwnedBy: "xiaomi",
		})
	}
	return result
}
