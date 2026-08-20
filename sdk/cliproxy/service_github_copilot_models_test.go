package cliproxy

import "testing"

func TestMergeCopilotModelsUsesAccountEntitlements(t *testing.T) {
	dynamic := []*ModelInfo{
		{ID: "gpt-5.6-sol"},
		{ID: "grok-4.6"},
		{ID: "account-only-model"},
	}
	static := []*ModelInfo{
		{ID: "gpt-5.6-sol"},
		{ID: "gpt-5.6-sol-cc"},
		{ID: "grok-4.6-cc"},
		{ID: "gpt-5.6-luna-cc"},
		{ID: "static-only-model"},
	}

	got := mergeCopilotModels(dynamic, static)
	assertCopilotModelIDs(t, got, []string{
		"gpt-5.6-sol",
		"grok-4.6",
		"account-only-model",
		"gpt-5.6-sol-cc",
		"grok-4.6-cc",
	})
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
