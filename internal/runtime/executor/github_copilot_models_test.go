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
