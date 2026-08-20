package registry

import "testing"

func TestConvertModelToMapClaudeIncludesContextMetadata(t *testing.T) {
	registry := &ModelRegistry{}
	model := &ModelInfo{
		ID:                  "test-cc",
		OwnedBy:             "test",
		ContextLength:       500000,
		MaxCompletionTokens: 128000,
		Thinking:            &ThinkingSupport{Levels: []string{"low", "high"}},
	}

	got := registry.convertModelToMap(model, "claude")
	if got["context_length"] != 500000 {
		t.Fatalf("context_length = %v, want 500000", got["context_length"])
	}
	if got["max_completion_tokens"] != 128000 {
		t.Fatalf("max_completion_tokens = %v, want 128000", got["max_completion_tokens"])
	}
	if got["thinking"] != true {
		t.Fatalf("thinking = %v, want true", got["thinking"])
	}
	if _, ok := got["extended_thinking"]; !ok {
		t.Fatal("extended_thinking missing")
	}
}

func TestConvertModelToMapClaudeOmitsZeroContextMetadata(t *testing.T) {
	registry := &ModelRegistry{}
	got := registry.convertModelToMap(&ModelInfo{ID: "test"}, "claude")

	if _, ok := got["context_length"]; ok {
		t.Fatal("context_length should be omitted when zero")
	}
	if _, ok := got["max_completion_tokens"]; ok {
		t.Fatal("max_completion_tokens should be omitted when zero")
	}
}
