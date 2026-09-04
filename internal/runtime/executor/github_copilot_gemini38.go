package executor

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func isGemini38ClaudeBridgeModel(model string) bool {
	base := strings.ToLower(strings.TrimSpace(copilotBaseModelName(model)))
	return base == "gemini-3.8-flash-cc"
}

// gemini38ChatBridgeModel selects the Gemini alias whenever the request will be
// sent through Chat Completions. OpenAI is included because the Responses
// handler normally converts Chat-only models before execution. OpenAI Responses
// is also included as a safe fallback for decorated aliases that registry lookup
// cannot resolve; the executor converts those requests instead of calling the
// unsupported upstream /responses endpoint.
func gemini38ChatBridgeModel(model string, sourceFormat sdktranslator.Format) (string, bool) {
	source := sourceFormat.String()
	if (source != "claude" && source != "openai" && source != "openai-response") || !isGemini38ClaudeBridgeModel(model) {
		return model, false
	}
	base := copilotBaseModelName(model)
	upstream := base[:len(base)-len(claudeBridgeSuffix)]
	return upstream, true
}

// normalizeGemini38RequestEffort clamps unsupported values before the generic
// thinking validator runs. The shared suffix parser has no "max" level, so the
// explicit Gemini mapping also makes (max)/(ultra) aliases reliably mean high.
func normalizeGemini38RequestEffort(body []byte, model string) []byte {
	body = normalizeCopilotEffort(body, model)
	suffix := thinking.ParseSuffix(model)
	if !suffix.HasSuffix {
		return body
	}
	switch strings.ToLower(strings.TrimSpace(suffix.RawSuffix)) {
	case "max", "ultra":
		if updated, err := sjson.SetBytes(body, "reasoning_effort", "high"); err == nil {
			return updated
		}
	case "auto", "-1":
		if updated, err := sjson.DeleteBytes(body, "reasoning_effort"); err == nil {
			return updated
		}
	}
	return body
}

// normalizeGemini38ReasoningResponse maps Copilot's Gemini-specific
// reasoning_text field to the standard reasoning_content field consumed by the
// OpenAI-to-Claude translators. The original field and reasoning_opaque token
// remain intact; tool-history requests work without caching the opaque token.
func normalizeGemini38ReasoningResponse(data []byte, model string) []byte {
	if !isGemini38ClaudeBridgeModel(model) {
		return data
	}

	payload := data
	isSSE := bytes.HasPrefix(data, dataTag)
	if isSSE {
		payload = bytes.TrimSpace(data[len(dataTag):])
	}
	if !gjson.ValidBytes(payload) {
		return data
	}

	changed := false
	choices := gjson.GetBytes(payload, "choices")
	for i := range choices.Array() {
		for _, container := range []string{"message", "delta"} {
			sourcePath := fmt.Sprintf("choices.%d.%s.reasoning_text", i, container)
			targetPath := fmt.Sprintf("choices.%d.%s.reasoning_content", i, container)
			reasoningText := gjson.GetBytes(payload, sourcePath)
			if !reasoningText.Exists() || gjson.GetBytes(payload, targetPath).Exists() {
				continue
			}
			updated, setErr := sjson.SetBytes(payload, targetPath, reasoningText.Value())
			if setErr != nil {
				continue
			}
			payload = updated
			changed = true
		}
	}
	if !changed {
		return data
	}
	if !isSSE {
		return payload
	}

	out := make([]byte, 0, len(data)+32)
	out = append(out, dataTag...)
	out = append(out, ' ')
	out = append(out, payload...)
	return out
}
