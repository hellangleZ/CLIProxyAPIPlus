package executor

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/tidwall/gjson"
)

func TestGPT6CopilotDefinitions(t *testing.T) {
	want := map[string]int{
		"gpt-6-sol-cc":    872000,
		"gpt-6-luna-cc":   872000,
		"gpt-6-astra-cc":  872000,
		"claude-opus-5.5": 1000000,
	}
	for _, model := range registry.GetGitHubCopilotModels() {
		contextLength, ok := want[model.ID]
		if !ok {
			continue
		}
		if model.ContextLength != contextLength {
			t.Errorf("%s context = %d, want %d", model.ID, model.ContextLength, contextLength)
		}
		if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/chat/completions" {
			t.Errorf("%s endpoints = %v", model.ID, model.SupportedEndpoints)
		}
		if model.Thinking == nil || len(model.Thinking.Levels) == 0 {
			t.Errorf("%s missing thinking metadata", model.ID)
		}
		delete(want, model.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing models: %v", want)
	}
}

func TestGPT6CopilotPromptLimitsAndCompact(t *testing.T) {
	for _, model := range []string{"gpt-6-sol-cc", "gpt-6-luna-cc", "gpt-6-astra-cc"} {
		for _, suffix := range []string{"", "[1m]", "(high)", "[1m](max)"} {
			t.Run(model+suffix, func(t *testing.T) {
				for _, errorBody := range []string{solContextWindowErrorBody, solPromptLimitErrorBody} {
					got := newGitHubCopilotStatusErr(http.StatusBadRequest, []byte(errorBody), model+suffix)
					if got.code != http.StatusBadRequest || !strings.HasPrefix(got.msg, standardPromptTooLongErrorText) {
						t.Fatalf("context error was not normalized: %#v", got)
					}
				}
				body := []byte(`{"tools":[{"type":"function","name":"Read"}],"reasoning":{"effort":"max"}}`)
				if got := normalizeCopilotEffort(body, model+suffix); string(got) != string(body) {
					t.Fatalf("native max effort changed: %s", got)
				}
				for _, statusCode := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
					got := newGitHubCopilotStatusErr(statusCode, []byte(solContextWindowErrorBody), model+suffix)
					if got.code != statusCode || got.msg != solContextWindowErrorBody {
						t.Fatalf("non-context status changed: %#v", got)
					}
				}
				payload := []byte(`{"messages":[{"role":"user","content":"CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\nYour task is to create a detailed summary of the conversation so far."}]}`)
				got := forceTextForClaudeCodeCompactRequest(body, payload, model+suffix)
				if gjson.GetBytes(got, "tool_choice").String() != "none" || gjson.GetBytes(got, "reasoning.effort").String() != "max" || gjson.GetBytes(got, "tools.#").Int() != 1 {
					t.Fatalf("compact changed thinking/tools or did not enforce text: %s", got)
				}
				if got := forceTextForClaudeCodeCompactRequest(body, []byte(`{"messages":[{"role":"user","content":"hello"}]}`), model+suffix); string(got) != string(body) {
					t.Fatalf("ordinary request changed: %s", got)
				}
			})
		}
	}
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna", "gpt-6-astra", "claude-opus-5.5", "gpt-5.6-luna-cc"} {
		got := newGitHubCopilotStatusErr(http.StatusBadRequest, []byte(solContextWindowErrorBody), model)
		if got.msg != solContextWindowErrorBody {
			t.Errorf("unrelated model %s error changed: %s", model, got.msg)
		}
	}
}

func TestCopilotOpus55DiscoveredLimits(t *testing.T) {
	models := parseCopilotModels([]byte(`{"data":[{"id":"claude-opus-5.5","capabilities":{"limits":{"max_prompt_tokens":900000,"max_output_tokens":100000}}}]}`))
	if len(models) != 1 || models[0].ContextLength != 900000 || models[0].MaxCompletionTokens != 100000 {
		t.Fatalf("upstream limits not preserved: %#v", models)
	}
}
