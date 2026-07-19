package executor

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeResponsesBridgeModel(t *testing.T) {
	claude := sdktranslator.FromString("claude")
	responses := sdktranslator.FromString("openai-response")

	cases := []struct {
		name         string
		model        string
		source       sdktranslator.Format
		wantUpstream string
		wantActive   bool
	}{
		{"sol bridge", "gpt-5.6-sol-cc", claude, "gpt-5.6-sol", true},
		{"luna bridge", "gpt-5.6-luna-cc", claude, "gpt-5.6-luna", true},
		{"terra bridge", "gpt-5.6-terra-cc", claude, "gpt-5.6-terra", true},
		{"bridge with thinking suffix", "gpt-5.6-sol-cc(high)", claude, "gpt-5.6-sol", true},
		{"non-claude source untouched", "gpt-5.6-sol-cc", responses, "gpt-5.6-sol-cc", false},
		{"plain gpt untouched", "gpt-5.6-sol", claude, "gpt-5.6-sol", false},
		{"claude model untouched", "claude-opus-4.8", claude, "claude-opus-4.8", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream, active := claudeResponsesBridgeModel(tc.model, tc.source)
			if active != tc.wantActive {
				t.Fatalf("active = %v, want %v", active, tc.wantActive)
			}
			if upstream != tc.wantUpstream {
				t.Fatalf("upstream = %q, want %q", upstream, tc.wantUpstream)
			}
		})
	}
}

func TestRewriteBridgeModel(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol-cc","input":[]}`)
	out := rewriteBridgeModel(body, "gpt-5.6-sol")
	if got := gjson.GetBytes(out, "model").String(); got != "gpt-5.6-sol" {
		t.Fatalf("model = %q, want gpt-5.6-sol", got)
	}

	// Empty upstream is a no-op.
	unchanged := rewriteBridgeModel(body, "")
	if got := gjson.GetBytes(unchanged, "model").String(); got != "gpt-5.6-sol-cc" {
		t.Fatalf("model = %q, want gpt-5.6-sol-cc (unchanged)", got)
	}
}

func TestWrapResponsesForCodexBridge(t *testing.T) {
	// Bare Copilot response object gets wrapped.
	bare := []byte(`{"object":"response","status":"completed","output":[{"type":"message"}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	wrapped := wrapResponsesForCodexBridge(bare)
	if got := gjson.GetBytes(wrapped, "type").String(); got != "response.completed" {
		t.Fatalf("type = %q, want response.completed", got)
	}
	if got := gjson.GetBytes(wrapped, "response.status").String(); got != "completed" {
		t.Fatalf("response.status = %q, want completed", got)
	}
	if got := gjson.GetBytes(wrapped, "response.output.0.type").String(); got != "message" {
		t.Fatalf("response.output.0.type = %q, want message", got)
	}

	// Already-wrapped body is returned unchanged.
	already := []byte(`{"type":"response.completed","response":{"status":"completed"}}`)
	out := wrapResponsesForCodexBridge(already)
	if got := gjson.GetBytes(out, "response.status").String(); got != "completed" {
		t.Fatalf("already-wrapped altered: %s", string(out))
	}
}
