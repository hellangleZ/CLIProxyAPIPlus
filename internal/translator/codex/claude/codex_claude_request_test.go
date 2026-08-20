package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCodexRepairsEmptyToolCallIDs(t *testing.T) {
	input := []byte(`{
		"messages": [
			{"role":"assistant","content":[{"type":"tool_use","id":"","name":"lookup","input":{"q":"x"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"","content":"ok"}]}
		],
		"tools": [{"name":"lookup","input_schema":{"type":"object","properties":{}}}]
	}`)

	output := ConvertClaudeRequestToCodex("gpt-5.6-sol-cc", input, true)
	items := gjson.GetBytes(output, "input").Array()

	var callID, outputCallID string
	for _, item := range items {
		switch item.Get("type").String() {
		case "function_call":
			callID = item.Get("call_id").String()
		case "function_call_output":
			outputCallID = item.Get("call_id").String()
		}
	}

	if callID == "" {
		t.Fatalf("function_call call_id is empty: %s", string(output))
	}
	if outputCallID != callID {
		t.Fatalf("function_call_output call_id = %q, want paired %q (body=%s)", outputCallID, callID, string(output))
	}
}
