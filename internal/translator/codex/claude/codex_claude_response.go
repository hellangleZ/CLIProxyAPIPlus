// Package claude provides response translation functionality for Codex to Claude Code API compatibility.
// This package handles the conversion of Codex API responses into Claude Code-compatible
// Server-Sent Events (SSE) format, implementing a sophisticated state machine that manages
// different response types including text content, thinking processes, and function calls.
// The translation ensures proper sequencing of SSE events and maintains state across
// multiple response chunks to provide a seamless streaming experience.
package claude

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertCodexResponseToClaudeParams holds parameters for response conversion.
type ConvertCodexResponseToClaudeParams struct {
	HasToolCall     bool
	BlockIndex      int
	Blocks          map[string]*claudeStreamBlock
	ToolAliases     map[string]string
	ToolCallIDs     map[string]string
	PendingToolArgs map[string][]string
	Completed       bool
}

type claudeStreamBlock struct {
	Index  int
	Kind   string
	Open   bool
	Closed bool
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func toolCallID(root gjson.Result) string {
	item := root.Get("item")
	if id := firstNonEmpty(
		item.Get("call_id").String(),
		root.Get("call_id").String(),
		item.Get("id").String(),
		root.Get("id").String(),
		root.Get("item_id").String(),
	); id != "" {
		return id
	}
	if outputIndex := root.Get("output_index"); outputIndex.Exists() {
		return "call_index_" + outputIndex.String()
	}
	return "call_unknown"
}

func toolCallIDFromKey(key string) string {
	id := strings.TrimPrefix(key, "tool:")
	if strings.HasPrefix(id, "output:") {
		if outputIndex := strings.TrimPrefix(id, "output:"); outputIndex != "" {
			return "call_index_" + outputIndex
		}
	}
	if id == "" {
		return "call_unknown"
	}
	return id
}

func toolBlockKey(state *ConvertCodexResponseToClaudeParams, root gjson.Result) string {
	item := root.Get("item")
	identities := []string{
		root.Get("item_id").String(), item.Get("id").String(),
		root.Get("call_id").String(), item.Get("call_id").String(),
	}
	if outputIndex := root.Get("output_index"); outputIndex.Exists() {
		key := "tool:output:" + outputIndex.String()
		if callID := firstNonEmpty(item.Get("call_id").String(), root.Get("call_id").String()); callID != "" {
			state.ToolCallIDs[key] = callID
		} else if state.ToolCallIDs[key] == "" {
			state.ToolCallIDs[key] = firstNonEmpty(item.Get("id").String(), root.Get("id").String(), root.Get("item_id").String())
		}
		for _, identity := range identities {
			if identity != "" {
				state.ToolAliases[identity] = key
			}
		}
		return key
	}
	for _, identity := range identities {
		if key := state.ToolAliases[identity]; identity != "" && key != "" {
			return key
		}
	}
	key := "tool:"
	for _, identity := range identities {
		if identity != "" {
			key += identity
			break
		}
	}
	if key == "tool:" {
		if outputIndex := root.Get("output_index"); outputIndex.Exists() {
			key += "call_index_" + outputIndex.String()
		} else {
			key += "call_unknown"
		}
	}
	for _, identity := range identities {
		if identity != "" {
			state.ToolAliases[identity] = key
		}
	}
	return key
}

func responseBlockKey(root gjson.Result, kind string) string {
	if outputIndex := root.Get("output_index"); outputIndex.Exists() {
		key := kind + ":output:" + outputIndex.String()
		switch kind {
		case "text":
			key += ":content:" + root.Get("content_index").String()
		case "thinking":
			key += ":summary:" + root.Get("summary_index").String()
		}
		return key
	}

	item := root.Get("item")
	for _, value := range []string{
		root.Get("item_id").String(),
		item.Get("id").String(),
		root.Get("call_id").String(),
		item.Get("call_id").String(),
	} {
		if value != "" {
			return kind + ":" + value
		}
	}
	return kind + ":unknown"
}

func ensureClaudeBlock(state *ConvertCodexResponseToClaudeParams, key, kind, startJSON string) (*claudeStreamBlock, string) {
	if block := state.Blocks[key]; block != nil {
		if block.Closed || block.Open {
			return block, ""
		}
	}
	block := &claudeStreamBlock{Index: state.BlockIndex, Kind: kind, Open: true}
	state.BlockIndex++
	state.Blocks[key] = block
	startJSON, _ = sjson.Set(startJSON, "index", block.Index)
	return block, "event: content_block_start\n" + fmt.Sprintf("data: %s\n\n", startJSON)
}

func closeOpenClaudeBlocks(state *ConvertCodexResponseToClaudeParams) string {
	blocks := make([]*claudeStreamBlock, 0, len(state.Blocks))
	for _, block := range state.Blocks {
		if block.Open && !block.Closed {
			blocks = append(blocks, block)
		}
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Index < blocks[j].Index })
	output := ""
	for _, block := range blocks {
		block.Open, block.Closed = false, true
		template := `{"type":"content_block_stop","index":0}`
		template, _ = sjson.Set(template, "index", block.Index)
		output += "event: content_block_stop\n" + fmt.Sprintf("data: %s\n\n", template)
	}
	return output
}

func closeClaudeBlock(state *ConvertCodexResponseToClaudeParams, key string) string {
	block := state.Blocks[key]
	if block == nil || !block.Open || block.Closed {
		return ""
	}
	block.Open = false
	block.Closed = true
	template := `{"type":"content_block_stop","index":0}`
	template, _ = sjson.Set(template, "index", block.Index)
	return "event: content_block_stop\n" + fmt.Sprintf("data: %s\n\n", template)
}

// ConvertCodexResponseToClaude performs sophisticated streaming response format conversion.
// This function implements a complex state machine that translates Codex API responses
// into Claude Code-compatible Server-Sent Events (SSE) format. It manages different response types
// and handles state transitions between content blocks, thinking processes, and function calls.
//
// Response type states: 0=none, 1=content, 2=thinking, 3=function
// The function maintains state across multiple calls to ensure proper SSE event sequencing.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - []string: A slice of strings, each containing a Claude Code-compatible JSON response
func ConvertCodexResponseToClaude(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &ConvertCodexResponseToClaudeParams{
			HasToolCall:     false,
			BlockIndex:      0,
			Blocks:          make(map[string]*claudeStreamBlock),
			ToolAliases:     make(map[string]string),
			ToolCallIDs:     make(map[string]string),
			PendingToolArgs: make(map[string][]string),
		}
	}

	// log.Debugf("rawJSON: %s", string(rawJSON))
	if !bytes.HasPrefix(rawJSON, dataTag) {
		return []string{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	output := ""
	rootResult := gjson.ParseBytes(rawJSON)
	typeResult := rootResult.Get("type")
	typeStr := typeResult.String()
	state := (*param).(*ConvertCodexResponseToClaudeParams)
	if state.Completed {
		return []string{""}
	}
	template := ""
	if typeStr == "response.created" {
		template = `{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"claude-opus-4-1-20250805","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0},"content":[],"stop_reason":null}}`
		template, _ = sjson.Set(template, "message.model", rootResult.Get("response.model").String())
		template, _ = sjson.Set(template, "message.id", rootResult.Get("response.id").String())

		output = "event: message_start\n"
		output += fmt.Sprintf("data: %s\n\n", template)
	} else if typeStr == "response.reasoning_summary_part.added" || typeStr == "response.reasoning_summary_text.delta" {
		state := (*param).(*ConvertCodexResponseToClaudeParams)
		key := responseBlockKey(rootResult, "thinking")
		block, start := ensureClaudeBlock(state, key, "thinking", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
		output = start
		if typeStr == "response.reasoning_summary_text.delta" && block.Open {
			template = `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`
			template, _ = sjson.Set(template, "index", block.Index)
			template, _ = sjson.Set(template, "delta.thinking", rootResult.Get("delta").String())
			output += "event: content_block_delta\n" + fmt.Sprintf("data: %s\n\n", template)
		}
	} else if typeStr == "response.reasoning_summary_part.done" {
		state := (*param).(*ConvertCodexResponseToClaudeParams)
		key := responseBlockKey(rootResult, "thinking")
		if state.Blocks[key] == nil {
			_, output = ensureClaudeBlock(state, key, "thinking", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
		}
		output += closeClaudeBlock(state, key)
	} else if typeStr == "response.content_part.added" || typeStr == "response.output_text.delta" {
		state := (*param).(*ConvertCodexResponseToClaudeParams)
		key := responseBlockKey(rootResult, "text")
		block, start := ensureClaudeBlock(state, key, "text", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		output = start
		if typeStr == "response.output_text.delta" && block.Open {
			template = `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`
			template, _ = sjson.Set(template, "index", block.Index)
			template, _ = sjson.Set(template, "delta.text", rootResult.Get("delta").String())
			output += "event: content_block_delta\n" + fmt.Sprintf("data: %s\n\n", template)
		}
	} else if typeStr == "response.content_part.done" {
		state := (*param).(*ConvertCodexResponseToClaudeParams)
		key := responseBlockKey(rootResult, "text")
		if state.Blocks[key] == nil {
			_, output = ensureClaudeBlock(state, key, "text", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		}
		output += closeClaudeBlock(state, key)
	} else if typeStr == "response.completed" {
		pendingKeys := make([]string, 0, len(state.PendingToolArgs))
		for key := range state.PendingToolArgs {
			pendingKeys = append(pendingKeys, key)
		}
		sort.Strings(pendingKeys)
		for _, key := range pendingKeys {
			deltas := state.PendingToolArgs[key]
			startJSON := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"unknown_tool","input":{}}}`
			callID := firstNonEmpty(state.ToolCallIDs[key], toolCallIDFromKey(key))
			startJSON, _ = sjson.Set(startJSON, "content_block.id", callID)
			block, start := ensureClaudeBlock(state, key, "tool", startJSON)
			output += start
			for _, delta := range deltas {
				deltaJSON := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`
				deltaJSON, _ = sjson.Set(deltaJSON, "index", block.Index)
				deltaJSON, _ = sjson.Set(deltaJSON, "delta.partial_json", delta)
				output += "event: content_block_delta\n" + fmt.Sprintf("data: %s\n\n", deltaJSON)
			}
			delete(state.PendingToolArgs, key)
		}
		output += closeOpenClaudeBlocks(state)
		template = `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`
		p := (*param).(*ConvertCodexResponseToClaudeParams).HasToolCall
		stopReason := rootResult.Get("response.stop_reason").String()
		if stopReason != "" {
			template, _ = sjson.Set(template, "delta.stop_reason", stopReason)
		} else if p {
			template, _ = sjson.Set(template, "delta.stop_reason", "tool_use")
		} else {
			template, _ = sjson.Set(template, "delta.stop_reason", "end_turn")
		}
		inputTokens, outputTokens, cachedTokens := extractResponsesUsage(rootResult.Get("response.usage"))
		template, _ = sjson.Set(template, "usage.input_tokens", inputTokens)
		template, _ = sjson.Set(template, "usage.output_tokens", outputTokens)
		if cachedTokens > 0 {
			template, _ = sjson.Set(template, "usage.cache_read_input_tokens", cachedTokens)
		}

		output += "event: message_delta\n"
		output += fmt.Sprintf("data: %s\n\n", template)
		output += "event: message_stop\n"
		output += `data: {"type":"message_stop"}`
		output += "\n\n"
		state.Completed = true
	} else if typeStr == "response.output_item.added" {
		itemResult := rootResult.Get("item")
		if itemResult.Get("type").String() == "function_call" {
			state := (*param).(*ConvertCodexResponseToClaudeParams)
			state.HasToolCall = true
			template = `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`
			template, _ = sjson.Set(template, "content_block.id", toolCallID(rootResult))
			name := itemResult.Get("name").String()
			if orig, ok := buildReverseMapFromClaudeOriginalShortToOriginal(originalRequestRawJSON)[name]; ok {
				name = orig
			}
			template, _ = sjson.Set(template, "content_block.name", name)
			key := toolBlockKey(state, rootResult)
			block, start := ensureClaudeBlock(state, key, "tool", template)
			output = start
			for _, delta := range state.PendingToolArgs[key] {
				deltaJSON := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`
				deltaJSON, _ = sjson.Set(deltaJSON, "index", block.Index)
				deltaJSON, _ = sjson.Set(deltaJSON, "delta.partial_json", delta)
				output += "event: content_block_delta\n" + fmt.Sprintf("data: %s\n\n", deltaJSON)
			}
			delete(state.PendingToolArgs, key)
		}
	} else if typeStr == "response.output_item.done" {
		if rootResult.Get("item.type").String() == "function_call" {
			state := (*param).(*ConvertCodexResponseToClaudeParams)
			output = closeClaudeBlock(state, toolBlockKey(state, rootResult))
		}
	} else if typeStr == "response.function_call_arguments.delta" {
		state := (*param).(*ConvertCodexResponseToClaudeParams)
		state.HasToolCall = true
		key := toolBlockKey(state, rootResult)
		if state.Blocks[key] == nil {
			state.PendingToolArgs[key] = append(state.PendingToolArgs[key], rootResult.Get("delta").String())
			return []string{""}
		}
		block := state.Blocks[key]
		if block.Open {
			template = `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`
			template, _ = sjson.Set(template, "index", block.Index)
			template, _ = sjson.Set(template, "delta.partial_json", rootResult.Get("delta").String())
			output += "event: content_block_delta\n" + fmt.Sprintf("data: %s\n\n", template)
		}
	} else if typeStr == "response.function_call_arguments.done" {
		state := (*param).(*ConvertCodexResponseToClaudeParams)
		output = closeClaudeBlock(state, toolBlockKey(state, rootResult))
	}

	return []string{output}
}

// ConvertCodexResponseToClaudeNonStream converts a non-streaming Codex response to a non-streaming Claude Code response.
// This function processes the complete Codex response and transforms it into a single Claude Code-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the Claude Code API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - string: A Claude Code-compatible JSON response containing all message content and metadata
func ConvertCodexResponseToClaudeNonStream(_ context.Context, _ string, originalRequestRawJSON, _ []byte, rawJSON []byte, _ *any) string {
	revNames := buildReverseMapFromClaudeOriginalShortToOriginal(originalRequestRawJSON)

	rootResult := gjson.ParseBytes(rawJSON)
	if rootResult.Get("type").String() != "response.completed" {
		return ""
	}

	responseData := rootResult.Get("response")
	if !responseData.Exists() {
		return ""
	}

	out := `{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`
	out, _ = sjson.Set(out, "id", responseData.Get("id").String())
	out, _ = sjson.Set(out, "model", responseData.Get("model").String())
	inputTokens, outputTokens, cachedTokens := extractResponsesUsage(responseData.Get("usage"))
	out, _ = sjson.Set(out, "usage.input_tokens", inputTokens)
	out, _ = sjson.Set(out, "usage.output_tokens", outputTokens)
	if cachedTokens > 0 {
		out, _ = sjson.Set(out, "usage.cache_read_input_tokens", cachedTokens)
	}

	hasToolCall := false

	if output := responseData.Get("output"); output.Exists() && output.IsArray() {
		output.ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "reasoning":
				thinkingBuilder := strings.Builder{}
				if summary := item.Get("summary"); summary.Exists() {
					if summary.IsArray() {
						summary.ForEach(func(_, part gjson.Result) bool {
							if txt := part.Get("text"); txt.Exists() {
								thinkingBuilder.WriteString(txt.String())
							} else {
								thinkingBuilder.WriteString(part.String())
							}
							return true
						})
					} else {
						thinkingBuilder.WriteString(summary.String())
					}
				}
				if thinkingBuilder.Len() == 0 {
					if content := item.Get("content"); content.Exists() {
						if content.IsArray() {
							content.ForEach(func(_, part gjson.Result) bool {
								if txt := part.Get("text"); txt.Exists() {
									thinkingBuilder.WriteString(txt.String())
								} else {
									thinkingBuilder.WriteString(part.String())
								}
								return true
							})
						} else {
							thinkingBuilder.WriteString(content.String())
						}
					}
				}
				if thinkingBuilder.Len() > 0 {
					block := `{"type":"thinking","thinking":""}`
					block, _ = sjson.Set(block, "thinking", thinkingBuilder.String())
					out, _ = sjson.SetRaw(out, "content.-1", block)
				}
			case "message":
				if content := item.Get("content"); content.Exists() {
					if content.IsArray() {
						content.ForEach(func(_, part gjson.Result) bool {
							if part.Get("type").String() == "output_text" {
								text := part.Get("text").String()
								if text != "" {
									block := `{"type":"text","text":""}`
									block, _ = sjson.Set(block, "text", text)
									out, _ = sjson.SetRaw(out, "content.-1", block)
								}
							}
							return true
						})
					} else {
						text := content.String()
						if text != "" {
							block := `{"type":"text","text":""}`
							block, _ = sjson.Set(block, "text", text)
							out, _ = sjson.SetRaw(out, "content.-1", block)
						}
					}
				}
			case "function_call":
				hasToolCall = true
				name := item.Get("name").String()
				if original, ok := revNames[name]; ok {
					name = original
				}

				toolBlock := `{"type":"tool_use","id":"","name":"","input":{}}`
				toolBlock, _ = sjson.Set(toolBlock, "id", toolCallID(item))
				toolBlock, _ = sjson.Set(toolBlock, "name", name)
				inputRaw := "{}"
				if argsStr := item.Get("arguments").String(); argsStr != "" && gjson.Valid(argsStr) {
					argsJSON := gjson.Parse(argsStr)
					if argsJSON.IsObject() {
						inputRaw = argsJSON.Raw
					}
				}
				toolBlock, _ = sjson.SetRaw(toolBlock, "input", inputRaw)
				out, _ = sjson.SetRaw(out, "content.-1", toolBlock)
			}
			return true
		})
	}

	if stopReason := responseData.Get("stop_reason"); stopReason.Exists() && stopReason.String() != "" {
		out, _ = sjson.Set(out, "stop_reason", stopReason.String())
	} else if hasToolCall {
		out, _ = sjson.Set(out, "stop_reason", "tool_use")
	} else {
		out, _ = sjson.Set(out, "stop_reason", "end_turn")
	}

	if stopSequence := responseData.Get("stop_sequence"); stopSequence.Exists() && stopSequence.String() != "" {
		out, _ = sjson.SetRaw(out, "stop_sequence", stopSequence.Raw)
	}

	return out
}

func extractResponsesUsage(usage gjson.Result) (int64, int64, int64) {
	if !usage.Exists() || usage.Type == gjson.Null {
		return 0, 0, 0
	}

	inputTokens := usage.Get("input_tokens").Int()
	outputTokens := usage.Get("output_tokens").Int()
	cachedTokens := usage.Get("input_tokens_details.cached_tokens").Int()

	if cachedTokens > 0 {
		if inputTokens >= cachedTokens {
			inputTokens -= cachedTokens
		} else {
			inputTokens = 0
		}
	}

	return inputTokens, outputTokens, cachedTokens
}

// buildReverseMapFromClaudeOriginalShortToOriginal builds a map[short]original from original Claude request tools.
func buildReverseMapFromClaudeOriginalShortToOriginal(original []byte) map[string]string {
	tools := gjson.GetBytes(original, "tools")
	rev := map[string]string{}
	if !tools.IsArray() {
		return rev
	}
	var names []string
	arr := tools.Array()
	for i := 0; i < len(arr); i++ {
		n := arr[i].Get("name").String()
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		m := buildShortNameMap(names)
		for orig, short := range m {
			rev[short] = orig
		}
	}
	return rev
}

func ClaudeTokenCount(ctx context.Context, count int64) string {
	return fmt.Sprintf(`{"input_tokens":%d}`, count)
}
