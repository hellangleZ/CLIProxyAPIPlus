package executor

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const defaultClaudeSubagentModel = "sonnet"

// grok46AgentModelPolicy is enabled only when the client declared an Agent
// schema that currently accepts our default. Reading the live schema avoids
// silently forcing a value after Claude Code changes its supported model set.
type grok46AgentModelPolicy struct {
	enabled bool
	allowed map[string]struct{}
}

func newGrok46AgentModelPolicy(bridgeActive bool, model string, originalRequest []byte) grok46AgentModelPolicy {
	if !bridgeActive || !strings.EqualFold(strings.TrimSpace(copilotBaseModelName(model)), "grok-4.6-cc") {
		return grok46AgentModelPolicy{}
	}

	allowed := make(map[string]struct{})
	tools := gjson.GetBytes(originalRequest, "tools")
	for _, tool := range tools.Array() {
		if tool.Get("name").String() != "Agent" {
			continue
		}
		for _, candidate := range tool.Get("input_schema.properties.model.enum").Array() {
			if candidate.Type == gjson.String {
				allowed[candidate.String()] = struct{}{}
			}
		}
		break
	}
	if _, acceptsDefault := allowed[defaultClaudeSubagentModel]; !acceptsDefault {
		return grok46AgentModelPolicy{}
	}
	return grok46AgentModelPolicy{enabled: true, allowed: allowed}
}

func (p grok46AgentModelPolicy) normalizeArguments(arguments string) string {
	if !p.enabled || !gjson.Valid(arguments) {
		return arguments
	}
	root := gjson.Parse(arguments)
	if !root.IsObject() {
		return arguments
	}
	model := root.Get("model")
	if model.Type == gjson.String {
		if _, allowed := p.allowed[model.String()]; allowed {
			return arguments
		}
	}
	updated, err := sjson.Set(arguments, "model", defaultClaudeSubagentModel)
	if err != nil {
		return arguments
	}
	return updated
}

func normalizeGrok46ClaudeAgentModels(payload []byte, policy grok46AgentModelPolicy) []byte {
	if !policy.enabled || !gjson.ValidBytes(payload) {
		return payload
	}
	content := gjson.GetBytes(payload, "content")
	for i, block := range content.Array() {
		if block.Get("type").String() != "tool_use" || block.Get("name").String() != "Agent" {
			continue
		}
		input := block.Get("input")
		if !input.IsObject() {
			continue
		}
		normalized := policy.normalizeArguments(input.Raw)
		if normalized == input.Raw {
			continue
		}
		updated, err := sjson.SetRawBytes(payload, fmt.Sprintf("content.%d.input", i), []byte(normalized))
		if err == nil {
			payload = updated
		}
	}
	return payload
}

type grok46AgentModelStreamNormalizer struct {
	policy         grok46AgentModelPolicy
	agentArguments map[int]*strings.Builder
}

func newGrok46AgentModelStreamNormalizer(policy grok46AgentModelPolicy) *grok46AgentModelStreamNormalizer {
	return &grok46AgentModelStreamNormalizer{
		policy:         policy,
		agentArguments: make(map[int]*strings.Builder),
	}
}

func (n *grok46AgentModelStreamNormalizer) normalize(chunk []byte) []byte {
	if n == nil || !n.policy.enabled || len(chunk) == 0 {
		return chunk
	}

	remaining := string(chunk)
	var output strings.Builder
	for remaining != "" {
		separator := strings.Index(remaining, "\n\n")
		if separator < 0 {
			output.WriteString(n.normalizeEvent(remaining))
			break
		}
		end := separator + 2
		output.WriteString(n.normalizeEvent(remaining[:end]))
		remaining = remaining[end:]
	}
	return []byte(output.String())
}

func (n *grok46AgentModelStreamNormalizer) normalizeEvent(event string) string {
	data := claudeSSEData(event)
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return event
	}
	root := gjson.ParseBytes(data)
	index := int(root.Get("index").Int())
	switch root.Get("type").String() {
	case "content_block_start":
		if root.Get("content_block.type").String() == "tool_use" && root.Get("content_block.name").String() == "Agent" {
			n.agentArguments[index] = &strings.Builder{}
		}
		return event
	case "content_block_delta":
		arguments := n.agentArguments[index]
		if arguments != nil && root.Get("delta.type").String() == "input_json_delta" {
			// A model value may be split across arbitrary SSE deltas. Hold only
			// Agent arguments until the complete JSON object is available.
			arguments.WriteString(root.Get("delta.partial_json").String())
			return ""
		}
		return event
	case "content_block_stop":
		arguments := n.agentArguments[index]
		if arguments == nil {
			return event
		}
		delete(n.agentArguments, index)
		return claudeInputJSONDeltaEvent(index, n.policy.normalizeArguments(arguments.String())) + event
	default:
		return event
	}
}

func claudeSSEData(event string) []byte {
	for _, line := range strings.Split(event, "\n") {
		if strings.HasPrefix(line, "data: ") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
		}
	}
	return nil
}

func claudeInputJSONDeltaEvent(index int, arguments string) string {
	if arguments == "" {
		return ""
	}
	payload := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`
	payload, _ = sjson.Set(payload, "index", index)
	payload, _ = sjson.Set(payload, "delta.partial_json", arguments)
	return "event: content_block_delta\ndata: " + payload + "\n\n"
}
