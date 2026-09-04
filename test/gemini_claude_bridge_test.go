package test

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestGemini38ClaudeBridgeModelCapabilities(t *testing.T) {
	var alias *registry.ModelInfo
	for _, model := range registry.GetGitHubCopilotModels() {
		if model.ID == "gemini-3.8-flash-cc" {
			alias = model
			break
		}
	}
	if alias == nil {
		t.Fatal("missing gemini-3.8-flash-cc model definition")
	}
	if alias.ContextLength != 983040 {
		t.Fatalf("context length = %d, want 983040", alias.ContextLength)
	}
	if alias.MaxCompletionTokens != 65536 {
		t.Fatalf("max completion tokens = %d, want 65536", alias.MaxCompletionTokens)
	}
	if fmt.Sprint(alias.SupportedEndpoints) != fmt.Sprint([]string{"/chat/completions"}) {
		t.Fatalf("supported endpoints = %v, want [/chat/completions]", alias.SupportedEndpoints)
	}
	if alias.Thinking == nil {
		t.Fatal("Gemini 3.8 alias has no thinking support")
	}
	if fmt.Sprint(alias.Thinking.Levels) != fmt.Sprint([]string{"low", "medium", "high"}) {
		t.Fatalf("thinking levels = %v, want [low medium high]", alias.Thinking.Levels)
	}
	if alias.Thinking.ZeroAllowed {
		t.Fatal("Gemini 3.8 alias incorrectly advertises disabled thinking")
	}
	if !alias.Thinking.DynamicAllowed {
		t.Fatal("Gemini 3.8 alias should preserve omitted/default adaptive reasoning")
	}
}
