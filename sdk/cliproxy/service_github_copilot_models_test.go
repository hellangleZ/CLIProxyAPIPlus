package cliproxy

import "testing"

func TestMergeCopilotModelsUsesAccountEntitlements(t *testing.T) {
	dynamic := []*ModelInfo{
		{ID: "gpt-5.6-sol"},
		{ID: "grok-4.6"},
		{ID: "gemini-3.8-flash", ContextLength: 983040, MaxCompletionTokens: 65536},
		{ID: "account-only-model"},
	}
	static := []*ModelInfo{
		{ID: "gpt-5.6-sol"},
		{ID: "gpt-5.6-sol-cc"},
		{ID: "grok-4.6-cc"},
		{ID: "gemini-3.8-flash-cc", ContextLength: 983040, MaxCompletionTokens: 65536},
		{ID: "gpt-5.6-luna-cc"},
		{ID: "static-only-model"},
	}

	got := mergeCopilotModels(dynamic, static)
	assertCopilotModelIDs(t, got, []string{
		"gpt-5.6-sol",
		"grok-4.6",
		"gemini-3.8-flash",
		"account-only-model",
		"gpt-5.6-sol-cc",
		"grok-4.6-cc",
		"gemini-3.8-flash-cc",
	})
}

func TestMergeCopilotModelsUsesAccountSpecificGemini38Limits(t *testing.T) {
	dynamic := []*ModelInfo{{
		ID:                  "gemini-3.8-flash",
		ContextLength:       200000,
		MaxCompletionTokens: 65536,
	}}
	static := []*ModelInfo{{
		ID:                  "gemini-3.8-flash-cc",
		ContextLength:       983040,
		MaxCompletionTokens: 65536,
		DisplayName:         "Gemini 3.8 Flash (Claude-compatible)",
	}}

	got := mergeCopilotModels(dynamic, static)
	if len(got) != 2 {
		t.Fatalf("model count = %d, want 2; got %#v", len(got), modelIDs(got))
	}
	alias := got[1]
	if alias.ID != "gemini-3.8-flash-cc" {
		t.Fatalf("alias ID = %q, want gemini-3.8-flash-cc", alias.ID)
	}
	if alias.ContextLength != 200000 {
		t.Fatalf("alias context length = %d, want account-specific 200000", alias.ContextLength)
	}
	if alias.MaxCompletionTokens != 65536 {
		t.Fatalf("alias max completion tokens = %d, want 65536", alias.MaxCompletionTokens)
	}
	if alias.DisplayName != "Gemini 3.8 Flash (Claude-compatible)" {
		t.Fatalf("alias display name changed: %q", alias.DisplayName)
	}
	if static[0].ContextLength != 983040 {
		t.Fatalf("static alias was mutated: context length = %d", static[0].ContextLength)
	}
}

func TestMergeCopilotModelsFallsBackWhenDiscoveryFails(t *testing.T) {
	static := []*ModelInfo{{ID: "static-model"}, {ID: "gpt-5.6-sol-cc"}}

	got := mergeCopilotModels(nil, static)
	assertCopilotModelIDs(t, got, []string{"static-model", "gpt-5.6-sol-cc"})
}

func TestMergeCopilotModelsMatchesBridgeBaseCaseInsensitively(t *testing.T) {
	dynamic := []*ModelInfo{{ID: "GPT-5.6-SOL"}, {ID: "gpt-5.6-sol"}}
	static := []*ModelInfo{{ID: "gpt-5.6-sol-cc"}}

	got := mergeCopilotModels(dynamic, static)
	assertCopilotModelIDs(t, got, []string{"GPT-5.6-SOL", "gpt-5.6-sol-cc"})
}

func assertCopilotModelIDs(t *testing.T, models []*ModelInfo, want []string) {
	t.Helper()
	if len(models) != len(want) {
		t.Fatalf("model count = %d, want %d; got %#v", len(models), len(want), modelIDs(models))
	}
	for i, model := range models {
		if model == nil || model.ID != want[i] {
			t.Fatalf("model[%d] = %#v, want %q", i, model, want[i])
		}
	}
}

func modelIDs(models []*ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model != nil {
			ids = append(ids, model.ID)
		}
	}
	return ids
}
