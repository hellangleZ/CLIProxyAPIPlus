package test

import (
	"fmt"
	"testing"
	"time"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/codex"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// TestGptClaudeBridgeThinking exercises the full request-side pipeline for the
// "-cc" Claude-compatible aliases of the Responses-only gpt-5.6-* models:
//
//	Claude /v1/messages body  →  TranslateRequest(claude→codex)  →  ApplyThinking(claude→codex)
//
// It asserts that every thinking budget a Claude client can send maps to a
// reasoning.effort value that GitHub Copilot's upstream actually accepts for the
// gpt-5.6 family: none / low / medium / high / xhigh / max. The bridge models are
// registered from the real static definitions so their ThinkingSupport.Levels are
// exactly what production uses.
func TestGptClaudeBridgeThinking(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("gpt-bridge-%d", time.Now().UnixNano())

	// Register only the "-cc" bridge models from the real static list.
	var bridge []*registry.ModelInfo
	for _, m := range registry.GetGitHubCopilotModels() {
		switch m.ID {
		case "gpt-5.6-sol-cc", "gpt-5.6-luna-cc", "gpt-5.6-terra-cc":
			bridge = append(bridge, m)
		}
	}
	if len(bridge) != 3 {
		t.Fatalf("expected 3 bridge models registered in static list, got %d", len(bridge))
	}
	reg.RegisterClient(uid, "github-copilot", bridge)
	defer reg.UnregisterClient(uid)

	// budget_tokens → expected upstream reasoning.effort.
	// Thresholds come from ConvertBudgetToLevel; the derived level is then clamped
	// to the model's supported set {low,medium,high,xhigh,max}.
	cases := []struct {
		name       string
		body       string
		wantEffort string
	}{
		{"disabled", `{"model":"gpt-5.6-sol-cc","max_tokens":2000,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`, "none"},
		{"budget-512-minimal-clamps-to-low", `{"model":"gpt-5.6-sol-cc","max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":512},"messages":[{"role":"user","content":"hi"}]}`, "low"},
		{"budget-1024-low", `{"model":"gpt-5.6-sol-cc","max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"}]}`, "low"},
		{"budget-8192-medium", `{"model":"gpt-5.6-sol-cc","max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":8192},"messages":[{"role":"user","content":"hi"}]}`, "medium"},
		{"budget-24576-high", `{"model":"gpt-5.6-sol-cc","max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":24576},"messages":[{"role":"user","content":"hi"}]}`, "high"},
		{"budget-32768-xhigh", `{"model":"gpt-5.6-sol-cc","max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":32768},"messages":[{"role":"user","content":"hi"}]}`, "xhigh"},
		{"budget-64000-xhigh-ceiling", `{"model":"gpt-5.6-sol-cc","max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":64000},"messages":[{"role":"user","content":"hi"}]}`, "xhigh"},
	}

	models := []string{"gpt-5.6-sol-cc", "gpt-5.6-luna-cc", "gpt-5.6-terra-cc"}
	for _, model := range models {
		for _, tc := range cases {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				body := replaceModel(tc.body, model)

				// Stage 1: Claude → Codex (Responses) request translation.
				translated := sdktranslator.TranslateRequest(
					sdktranslator.FromString("claude"),
					sdktranslator.FromString("codex"),
					model,
					[]byte(body),
					false,
				)

				// Stage 2: thinking config applied for the codex target.
				out, err := thinking.ApplyThinking(translated, model, "claude", "codex", "github-copilot")
				if err != nil {
					t.Fatalf("ApplyThinking error: %v (body=%s)", err, string(translated))
				}

				got := gjson.GetBytes(out, "reasoning.effort").String()
				if got != tc.wantEffort {
					t.Fatalf("reasoning.effort = %q, want %q (body=%s)", got, tc.wantEffort, string(out))
				}

				// Sanity: the translated body must carry the alias model name;
				// the executor rewrites it to the real upstream name afterwards.
				if m := gjson.GetBytes(out, "model").String(); m != model {
					t.Fatalf("translated model = %q, want %q", m, model)
				}
			})
		}
	}
}

// TestGptClaudeBridgeExplicitEffort verifies the "output_config.effort" Claude
// format (used by newer Claude clients) maps straight through to reasoning.effort,
// including the top "max" tier that Copilot accepts for gpt-5.6.
func TestGptClaudeBridgeExplicitEffort(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("gpt-bridge-eff-%d", time.Now().UnixNano())

	var bridge []*registry.ModelInfo
	for _, m := range registry.GetGitHubCopilotModels() {
		if m.ID == "gpt-5.6-sol-cc" {
			bridge = append(bridge, m)
		}
	}
	reg.RegisterClient(uid, "github-copilot", bridge)
	defer reg.UnregisterClient(uid)

	cases := []struct {
		effort     string
		wantEffort string
	}{
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"xhigh", "xhigh"},
		{"max", "max"},
	}
	for _, tc := range cases {
		t.Run(tc.effort, func(t *testing.T) {
			body := fmt.Sprintf(`{"model":"gpt-5.6-sol-cc","max_tokens":2000,"output_config":{"effort":%q},"messages":[{"role":"user","content":"hi"}]}`, tc.effort)
			translated := sdktranslator.TranslateRequest(
				sdktranslator.FromString("claude"),
				sdktranslator.FromString("codex"),
				"gpt-5.6-sol-cc",
				[]byte(body),
				false,
			)
			out, err := thinking.ApplyThinking(translated, "gpt-5.6-sol-cc", "claude", "codex", "github-copilot")
			if err != nil {
				t.Fatalf("ApplyThinking error: %v", err)
			}
			if got := gjson.GetBytes(out, "reasoning.effort").String(); got != tc.wantEffort {
				t.Fatalf("effort %q -> reasoning.effort %q, want %q (body=%s)", tc.effort, got, tc.wantEffort, string(out))
			}
		})
	}
}

func replaceModel(body, model string) string {
	out, _ := sjson.Set(body, "model", model)
	return out
}
