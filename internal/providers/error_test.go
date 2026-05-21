package providers

import "testing"

func TestIsTooLongError(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "exact MiMo message",
			content: "Sorry, the text you sent is too long! I suggest you simplify the content appropriately or send it in parts. Thank you for your understanding.",
			want:    true,
		},
		{
			name:    "partial match",
			content: "Error: the text you sent is too long",
			want:    true,
		},
		{
			name:    "case insensitive",
			content: "SORRY, THE TEXT YOU SENT IS TOO LONG",
			want:    true,
		},
		{
			name:    "send it in parts",
			content: "Please send it in parts",
			want:    true,
		},
		{
			name:    "normal response",
			content: "Here is the answer to your question about Go programming.",
			want:    false,
		},
		{
			name:    "empty string",
			content: "",
			want:    false,
		},
		{
			name:    "tool call content",
			content: `{"name":"search","arguments":{"q":"test"}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTooLongError(tt.content); got != tt.want {
				t.Errorf("IsTooLongError(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}
