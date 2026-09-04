package auth

import "testing"

func TestModelKeyForAuthMatching(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "plain", model: "gpt-5.6-sol-cc", want: "gpt-5.6-sol-cc"},
		{name: "thinking suffix", model: "gpt-5.6-sol-cc(high)", want: "gpt-5.6-sol-cc"},
		{name: "context tag", model: "gpt-5.6-sol-cc[1m]", want: "gpt-5.6-sol-cc"},
		{name: "context tag before thinking suffix", model: "gpt-5.6-sol-cc[1M](high)", want: "gpt-5.6-sol-cc"},
		{name: "gemini context and thinking suffix", model: "gemini-3.8-flash-cc[1m](high)", want: "gemini-3.8-flash-cc"},
		{name: "unknown tag preserved", model: "custom-model[preview]", want: "custom-model[preview]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelKeyForAuthMatching(tt.model); got != tt.want {
				t.Fatalf("modelKeyForAuthMatching(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}
