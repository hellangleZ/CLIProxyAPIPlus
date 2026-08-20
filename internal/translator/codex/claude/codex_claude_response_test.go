package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToClaudeStartsBlocksBeforeDeltas(t *testing.T) {
	cases := []struct {
		name   string
		events []string
	}{
		{
			name: "text delta before added",
			events: []string{
				`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hello"}`,
				`{"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0}`,
			},
		},
		{
			name: "reasoning delta before added",
			events: []string{
				`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"think"}`,
				`{"type":"response.reasoning_summary_part.done","item_id":"rs_1","output_index":0,"summary_index":0}`,
			},
		},
		{
			name: "tool delta before added",
			events: []string{
				`{"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_1","output_index":0,"delta":"{\"q\":"}`,
				`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup"}}`,
				`{"type":"response.function_call_arguments.done","item_id":"fc_1","call_id":"call_1","output_index":0}`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertValidClaudeBlockLifecycle(t, translateCodexEvents(tc.events))
		})
	}
}

func TestConvertCodexResponseToClaudeInterleavedTools(t *testing.T) {
	events := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"first"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_2","type":"function_call","call_id":"call_2","name":"second"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_1","output_index":0,"delta":"{\"a\":1}"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_2","call_id":"call_2","output_index":1,"delta":"{\"b\":2}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_2","type":"function_call","call_id":"call_2"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1"}}`,
	}

	outputs := translateCodexEvents(events)
	assertValidClaudeBlockLifecycle(t, outputs)

	indexesByText := map[string]int64{}
	for _, output := range outputs {
		if gjson.Get(output, "type").String() != "content_block_delta" {
			continue
		}
		partial := gjson.Get(output, "delta.partial_json").String()
		if partial != "" {
			indexesByText[partial] = gjson.Get(output, "index").Int()
		}
	}
	if indexesByText[`{"a":1}`] == indexesByText[`{"b":2}`] {
		t.Fatalf("interleaved tool deltas used the same index: %v", indexesByText)
	}
}

func TestConvertCodexResponseToClaudeUsesNonEmptyToolIDFallback(t *testing.T) {
	events := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","name":"lookup"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"q\":\"x\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","name":"lookup"}}`,
	}

	outputs := translateCodexEvents(events)
	assertValidClaudeBlockLifecycle(t, outputs)

	for _, output := range outputs {
		if gjson.Get(output, "type").String() != "content_block_start" {
			continue
		}
		block := gjson.Get(output, "content_block")
		if block.Get("type").String() != "tool_use" {
			continue
		}
		if got := block.Get("id").String(); got != "fc_1" {
			t.Fatalf("tool_use id = %q, want fallback item id fc_1 (output=%s)", got, output)
		}
		return
	}
	t.Fatal("missing tool_use content block")
}

func TestConvertCodexResponseToClaudePendingToolFallbackUsesNonEmptyID(t *testing.T) {
	events := []string{
		`{"type":"response.function_call_arguments.delta","item_id":"fc_pending","output_index":0,"delta":"{\"q\":\"x\"}"}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-sol","usage":{"input_tokens":1,"output_tokens":2}}}`,
	}

	outputs := translateCodexEvents(events)

	for _, output := range outputs {
		if gjson.Get(output, "type").String() != "content_block_start" {
			continue
		}
		block := gjson.Get(output, "content_block")
		if block.Get("type").String() != "tool_use" {
			continue
		}
		if got := block.Get("id").String(); got != "fc_pending" {
			t.Fatalf("pending tool_use id = %q, want fallback item id fc_pending (output=%s)", got, output)
		}
		return
	}
	t.Fatal("missing pending tool_use content block")
}

func TestConvertCodexResponseToClaudeDuplicateLifecycleIsIdempotent(t *testing.T) {
	events := []string{
		`{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0}`,
		`{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hello"}`,
		`{"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0}`,
		`{"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0}`,
	}
	outputs := translateCodexEvents(events)
	assertValidClaudeBlockLifecycle(t, outputs)

	starts, stops := 0, 0
	for _, output := range outputs {
		switch gjson.Get(output, "type").String() {
		case "content_block_start":
			starts++
		case "content_block_stop":
			stops++
		}
	}
	if starts != 1 || stops != 1 {
		t.Fatalf("starts=%d stops=%d, want 1 each", starts, stops)
	}
}

func TestConvertCodexResponseToClaudeKeepsBlocksStableWhenItemIDsChange(t *testing.T) {
	events := []string{
		`{"type":"response.reasoning_summary_part.added","item_id":"reasoning-added","output_index":0,"summary_index":0}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"reasoning-delta-1","output_index":0,"summary_index":0,"delta":"think"}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"reasoning-delta-2","output_index":0,"summary_index":0,"delta":"ing"}`,
		`{"type":"response.reasoning_summary_part.done","item_id":"reasoning-done","output_index":0,"summary_index":0}`,
		`{"type":"response.content_part.added","item_id":"text-added","output_index":1,"content_index":0}`,
		`{"type":"response.output_text.delta","item_id":"text-delta-1","output_index":1,"content_index":0,"delta":"hel"}`,
		`{"type":"response.output_text.delta","item_id":"text-delta-2","output_index":1,"content_index":0,"delta":"lo"}`,
		`{"type":"response.content_part.done","item_id":"text-done","output_index":1,"content_index":0}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-luna","usage":{"input_tokens":1,"output_tokens":2}}}`,
	}

	outputs := translateCodexEvents(events)
	assertValidClaudeBlockLifecycle(t, outputs)

	startsByKind := map[string]int{}
	for _, output := range outputs {
		if gjson.Get(output, "type").String() != "content_block_start" {
			continue
		}
		startsByKind[gjson.Get(output, "content_block.type").String()]++
	}
	if startsByKind["thinking"] != 1 || startsByKind["text"] != 1 {
		t.Fatalf("content block starts = %v, want one thinking and one text block", startsByKind)
	}
}

func TestConvertCodexResponseToClaudeKeepsToolBlockStableWhenItemIDsChange(t *testing.T) {
	events := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"tool-added","type":"function_call","call_id":"call_1","name":"lookup"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"tool-delta-1","output_index":0,"delta":"{\"q\":"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"tool-delta-2","output_index":0,"delta":"\"x\"}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"tool-args-done","output_index":0}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"tool-done","type":"function_call","call_id":"call_1","name":"lookup"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-luna","usage":{"input_tokens":1,"output_tokens":2}}}`,
	}

	outputs := translateCodexEvents(events)
	assertValidClaudeBlockLifecycle(t, outputs)

	starts, deltas := 0, 0
	for _, output := range outputs {
		switch gjson.Get(output, "type").String() {
		case "content_block_start":
			if gjson.Get(output, "content_block.type").String() != "tool_use" {
				continue
			}
			starts++
			if got := gjson.Get(output, "content_block.id").String(); got != "call_1" {
				t.Fatalf("tool id = %q, want call_1", got)
			}
			if got := gjson.Get(output, "content_block.name").String(); got != "lookup" {
				t.Fatalf("tool name = %q, want lookup", got)
			}
		case "content_block_delta":
			if gjson.Get(output, "delta.type").String() == "input_json_delta" {
				deltas++
			}
		}
	}
	if starts != 1 || deltas != 2 {
		t.Fatalf("tool starts=%d deltas=%d, want one start and two deltas", starts, deltas)
	}
}

func translateCodexEvents(events []string) []string {
	var param any
	var outputs []string
	for _, event := range events {
		chunks := ConvertCodexResponseToClaude(context.Background(), "test", nil, nil, []byte("data: "+event), &param)
		for _, chunk := range chunks {
			for _, section := range strings.Split(chunk, "\n\n") {
				for _, line := range strings.Split(section, "\n") {
					if strings.HasPrefix(line, "data: ") {
						outputs = append(outputs, strings.TrimPrefix(line, "data: "))
					}
				}
			}
		}
	}
	return outputs
}

func assertValidClaudeBlockLifecycle(t *testing.T, outputs []string) {
	t.Helper()
	open := map[int64]bool{}
	started := map[int64]bool{}
	for _, output := range outputs {
		typeName := gjson.Get(output, "type").String()
		index := gjson.Get(output, "index").Int()
		switch typeName {
		case "content_block_start":
			if started[index] {
				t.Fatalf("duplicate start for index %d: %s", index, output)
			}
			started[index], open[index] = true, true
		case "content_block_delta":
			if !open[index] {
				t.Fatalf("delta for unopened index %d: %s", index, output)
			}
		case "content_block_stop":
			if !open[index] {
				t.Fatalf("stop for unopened index %d: %s", index, output)
			}
			open[index] = false
		}
	}
	for index, isOpen := range open {
		if isOpen {
			t.Fatalf("block index %d remained open", index)
		}
	}
}
