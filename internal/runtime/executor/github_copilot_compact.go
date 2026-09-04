package executor

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	claudeCodeCompactPromptPrefix = "CRITICAL: Respond with TEXT ONLY. Do NOT call any tools."
	claudeCodeCompactTaskMarker   = "Your task is to create a detailed summary of the conversation so far."
)

// forceTextForClaudeCodeCompactRequest enforces Claude Code's text-only compact
// contract while preserving tool definitions, history, and thinking settings.
// Only models confirmed to need this guard are selected.
func forceTextForClaudeCodeCompactRequest(body, originalPayload []byte, model string) []byte {
	if (!isGPT56SolClaudeBridgeModel(model) && !isGemini38ClaudeBridgeModel(model)) || !isClaudeCodeCompactRequest(originalPayload) {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return body
	}
	if updated, err := sjson.SetBytes(body, "tool_choice", "none"); err == nil {
		return updated
	}
	return body
}

func isClaudeCodeCompactRequest(payload []byte) bool {
	if !bytes.Contains(payload, []byte(claudeCodeCompactPromptPrefix)) {
		return false
	}
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return false
	}
	var latestUser gjson.Result
	messages.ForEach(func(_, message gjson.Result) bool {
		if strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "user") {
			latestUser = message
		}
		return true
	})
	content := latestUser.Get("content")
	if content.Type == gjson.String {
		return isClaudeCodeCompactPrompt(content.String())
	}
	if content.IsArray() {
		for _, block := range content.Array() {
			if block.Get("type").String() == "text" && isClaudeCodeCompactPrompt(block.Get("text").String()) {
				return true
			}
		}
	}
	return false
}

func isClaudeCodeCompactPrompt(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, claudeCodeCompactPromptPrefix) && strings.Contains(text, claudeCodeCompactTaskMarker)
}
