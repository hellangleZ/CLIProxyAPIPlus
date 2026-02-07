package executor

import (
	"sort"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestParseCopilotModelsDataObjects(t *testing.T) {
	body := []byte(`{"data":[{"id":"claude-opus-4.6","owned_by":"github-copilot","created":123},{"id":"gpt-5"}]}`)
	models := parseCopilotModels(body)
	ids := collectModelIDs(models)
	if len(ids) != 2 {
		t.Fatalf("expected 2 models, got %d", len(ids))
	}
	if ids[0] != "claude-opus-4.6" || ids[1] != "gpt-5" {
		t.Fatalf("unexpected ids: %v", ids)
	}
}

func TestParseCopilotModelsDataStrings(t *testing.T) {
	body := []byte(`{"data":["claude-opus-4.6","gpt-5"]}`)
	models := parseCopilotModels(body)
	ids := collectModelIDs(models)
	if len(ids) != 2 {
		t.Fatalf("expected 2 models, got %d", len(ids))
	}
	if ids[0] != "claude-opus-4.6" || ids[1] != "gpt-5" {
		t.Fatalf("unexpected ids: %v", ids)
	}
}

func TestParseCopilotModelsModelsMap(t *testing.T) {
	body := []byte(`{"models":{"gpt-5":{},"claude-opus-4.6":{}}}`)
	models := parseCopilotModels(body)
	ids := collectSortedModelIDs(models)
	if len(ids) != 2 {
		t.Fatalf("expected 2 models, got %d", len(ids))
	}
	if ids[0] != "claude-opus-4.6" || ids[1] != "gpt-5" {
		t.Fatalf("unexpected ids: %v", ids)
	}
}

func TestParseCopilotModelsCreatedAtFallback(t *testing.T) {
	body := []byte(`{"data":[{"id":"claude-opus-4.6","created_at":456}]}`)
	models := parseCopilotModels(body)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].Created != 456 {
		t.Fatalf("expected created 456, got %d", models[0].Created)
	}
}

func TestParseCopilotModelsVendorField(t *testing.T) {
	body := []byte(`{"data":[{"id":"claude-opus-4.6","vendor":"anthropic"},{"id":"gpt-5","owned_by":"openai"}]}`)
	models := parseCopilotModels(body)
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].OwnedBy != "anthropic" {
		t.Fatalf("expected vendor 'anthropic' for claude-opus-4.6, got %q", models[0].OwnedBy)
	}
	if models[1].OwnedBy != "openai" {
		t.Fatalf("expected owned_by 'openai' for gpt-5, got %q", models[1].OwnedBy)
	}
}

func TestParseCopilotModelsSupportedEndpointsFromStatic(t *testing.T) {
	// Models in the static list should inherit SupportedEndpoints
	body := []byte(`{"data":[{"id":"claude-opus-4.6"},{"id":"claude-sonnet-4.5"},{"id":"gpt-5"},{"id":"gpt-5-codex"}]}`)
	models := parseCopilotModels(body)
	if len(models) != 4 {
		t.Fatalf("expected 4 models, got %d", len(models))
	}

	// claude-opus-4.6 should have /chat/completions from static list
	assertEndpoints(t, models[0], []string{"/chat/completions"})

	// claude-sonnet-4.5 should have /chat/completions from static list
	assertEndpoints(t, models[1], []string{"/chat/completions"})

	// gpt-5 should have both endpoints from static list
	assertEndpoints(t, models[2], []string{"/chat/completions", "/responses"})

	// gpt-5-codex should have /responses only from static list
	assertEndpoints(t, models[3], []string{"/responses"})
}

func TestParseCopilotModelsStaticMetadataMerged(t *testing.T) {
	// Verify that ContextLength and MaxCompletionTokens are merged from static definitions
	body := []byte(`{"data":[{"id":"claude-opus-4.6"}]}`)
	models := parseCopilotModels(body)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	m := models[0]
	if m.ContextLength != 200000 {
		t.Errorf("expected ContextLength 200000 from static, got %d", m.ContextLength)
	}
	if m.MaxCompletionTokens != 64000 {
		t.Errorf("expected MaxCompletionTokens 64000 from static, got %d", m.MaxCompletionTokens)
	}
	if m.DisplayName != "Claude Opus 4.6" {
		t.Errorf("expected DisplayName 'Claude Opus 4.6' from static, got %q", m.DisplayName)
	}
	// CRITICAL: Verify Thinking field is NOT set for GitHub Copilot Claude models
	// (GitHub Copilot does not support output_config.effort)
	if m.Thinking != nil {
		t.Errorf("expected Thinking to be nil for GitHub Copilot models, got %+v", m.Thinking)
	}
}

func TestParseCopilotModelsInferEndpointsForUnknownModel(t *testing.T) {
	// Models not in the static list should get inferred endpoints
	body := []byte(`{"data":[{"id":"claude-new-model"},{"id":"gpt-5.3"},{"id":"gpt-5.3-codex"},{"id":"o4-mini"}]}`)
	models := parseCopilotModels(body)
	if len(models) != 4 {
		t.Fatalf("expected 4 models, got %d", len(models))
	}

	// Unknown Claude model → /chat/completions
	assertEndpoints(t, models[0], []string{"/chat/completions"})

	// Unknown gpt-5.x model → /responses (GPT/OpenAI series via Copilot)
	assertEndpoints(t, models[1], []string{"/responses"})

	// Unknown codex model → /responses only
	assertEndpoints(t, models[2], []string{"/responses"})

	// o4-mini → /responses (OpenAI reasoning series via Copilot)
	assertEndpoints(t, models[3], []string{"/responses"})
}

func TestParseCopilotModelsInferDefaultContextLength(t *testing.T) {
	// Models not in the static list should get default context length
	body := []byte(`{"data":[{"id":"some-new-model"}]}`)
	models := parseCopilotModels(body)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ContextLength != 200000 {
		t.Errorf("expected default ContextLength 200000, got %d", models[0].ContextLength)
	}
	if models[0].MaxCompletionTokens != 64000 {
		t.Errorf("expected default MaxCompletionTokens 64000, got %d", models[0].MaxCompletionTokens)
	}
	// Unknown models should also have Thinking set to nil
	if models[0].Thinking != nil {
		t.Errorf("expected Thinking to be nil for unknown models, got %+v", models[0].Thinking)
	}
}

func TestFindStaticCopilotModel(t *testing.T) {
	// Should find known models
	m := findStaticCopilotModel("claude-opus-4.6")
	if m == nil {
		t.Fatal("expected to find claude-opus-4.6 in static list")
	}
	if len(m.SupportedEndpoints) == 0 {
		t.Fatal("expected SupportedEndpoints to be set for claude-opus-4.6")
	}

	// Should return nil for unknown models
	m = findStaticCopilotModel("nonexistent-model-xyz")
	if m != nil {
		t.Fatal("expected nil for unknown model")
	}
}

func TestInferSupportedEndpoints(t *testing.T) {
	tests := []struct {
		modelID  string
		expected []string
	}{
		{"claude-opus-5", []string{"/chat/completions"}},
		{"claude-sonnet-5", []string{"/chat/completions"}},
		{"gemini-3-pro", []string{"/chat/completions"}},
		{"gpt-5.5", []string{"/responses"}},
		{"gpt-5.5-mini", []string{"/responses"}},
		{"gpt-5.5-codex", []string{"/responses"}},
		{"o1-preview", []string{"/responses"}},
		{"o3-mini", []string{"/responses"}},
		{"o4-mini", []string{"/responses"}},
		{"some-random-model", []string{"/chat/completions"}},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			got := inferSupportedEndpoints(tt.modelID)
			assertStringSlicesEqual(t, got, tt.expected)
		})
	}
}

func collectModelIDs(models []*registry.ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model != nil {
			ids = append(ids, model.ID)
		}
	}
	return ids
}

func collectSortedModelIDs(models []*registry.ModelInfo) []string {
	ids := collectModelIDs(models)
	sort.Strings(ids)
	return ids
}

func assertEndpoints(t *testing.T, model *registry.ModelInfo, expected []string) {
	t.Helper()
	if len(model.SupportedEndpoints) != len(expected) {
		t.Errorf("model %s: expected %d endpoints %v, got %d endpoints %v",
			model.ID, len(expected), expected, len(model.SupportedEndpoints), model.SupportedEndpoints)
		return
	}
	for i, ep := range expected {
		if model.SupportedEndpoints[i] != ep {
			t.Errorf("model %s: endpoint[%d] expected %q, got %q",
				model.ID, i, ep, model.SupportedEndpoints[i])
		}
	}
}

func assertStringSlicesEqual(t *testing.T, got, expected []string) {
	t.Helper()
	if len(got) != len(expected) {
		t.Errorf("expected %v, got %v", expected, got)
		return
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("index %d: expected %q, got %q", i, expected[i], got[i])
		}
	}
}
