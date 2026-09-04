package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

const testClaudeAgentInputSchema = `{"type":"object","properties":{"description":{"type":"string"},"prompt":{"type":"string"},"model":{"type":"string","enum":["sonnet","opus","haiku","fable"]}},"required":["description","prompt"],"additionalProperties":false}`

func TestGitHubCopilotClaudeBridgeDefaultsGrok46AgentModel(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		toolName  string
		arguments string
		wantModel string
		wantSet   bool
	}{
		{
			name:      "invalid fast defaults to sonnet",
			model:     "grok-4.6-cc",
			toolName:  "Agent",
			arguments: `{"description":"Review code","prompt":"Inspect the change","subagent_type":"Explore","model":"fast"}`,
			wantModel: "sonnet",
			wantSet:   true,
		},
		{
			name:      "missing model defaults to sonnet",
			model:     "grok-4.6-cc[1m](high)",
			toolName:  "Agent",
			arguments: `{"description":"Review code","prompt":"Inspect the change","subagent_type":"Explore"}`,
			wantModel: "sonnet",
			wantSet:   true,
		},
		{
			name:      "valid explicit model is preserved",
			model:     "grok-4.6-cc",
			toolName:  "Agent",
			arguments: `{"description":"Review code","prompt":"Inspect the change","subagent_type":"Explore","model":"haiku"}`,
			wantModel: "haiku",
			wantSet:   true,
		},
		{
			name:      "other tool is untouched",
			model:     "grok-4.6-cc",
			toolName:  "Bash",
			arguments: `{"command":"echo ok","model":"fast"}`,
			wantModel: "fast",
			wantSet:   true,
		},
		{
			name:      "other bridge model is untouched",
			model:     "gpt-5.6-sol-cc",
			toolName:  "Agent",
			arguments: `{"description":"Review code","prompt":"Inspect the change","subagent_type":"Explore","model":"fast"}`,
			wantModel: "fast",
			wantSet:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := githubCopilotRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				response := fmt.Sprintf(`{"id":"resp-agent-default","object":"response","model":"upstream-model","output":[{"type":"function_call","call_id":"call_agent_1","name":%q,"arguments":%q}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}`, tc.toolName, tc.arguments)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(response)),
					Request:    req,
				}, nil
			})
			installGitHubCopilotTestTransport(t, transport)
			executor, auth := newGitHubCopilotTestExecutorAndAuth()

			inputSchema := `{"type":"object"}`
			if tc.toolName == "Agent" {
				inputSchema = testClaudeAgentInputSchema
			}
			payload := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":256,"messages":[{"role":"user","content":"review"}],"tools":[{"name":%q,"description":"test tool","input_schema":%s}]}`, tc.model, tc.toolName, inputSchema))
			resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: tc.model, Payload: payload}, cliproxyexecutor.Options{
				OriginalRequest: payload,
				SourceFormat:    sdktranslator.FromString("claude"),
			})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}

			modelResult := gjson.GetBytes(resp.Payload, `content.#(type=="tool_use").input.model`)
			if modelResult.Exists() != tc.wantSet {
				t.Fatalf("model existence = %v, want %v (body=%s)", modelResult.Exists(), tc.wantSet, string(resp.Payload))
			}
			if got := modelResult.String(); got != tc.wantModel {
				t.Fatalf("model = %q, want %q (body=%s)", got, tc.wantModel, string(resp.Payload))
			}
		})
	}
}

func TestGitHubCopilotClaudeBridgeDefaultsSplitGrok46AgentModelStream(t *testing.T) {
	transport := githubCopilotRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		response := strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp-agent-stream","model":"grok-4.6","output":[]}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_agent_1","call_id":"call_agent_1","name":"Agent","arguments":"","status":"in_progress"}}`,
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_agent_1","delta":"{\"description\":\"Review code\",\"prompt\":\"Inspect the change\",\"subagent_type\":\"Explore\",\"model\":\"fa"}`,
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_agent_1","delta":"st\"}"}`,
			`data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_agent_1","arguments":"{\"description\":\"Review code\",\"prompt\":\"Inspect the change\",\"subagent_type\":\"Explore\",\"model\":\"fast\"}"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_agent_1","call_id":"call_agent_1","name":"Agent","arguments":"{\"description\":\"Review code\",\"prompt\":\"Inspect the change\",\"subagent_type\":\"Explore\",\"model\":\"fast\"}","status":"completed"}}`,
			`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_agent_2","call_id":"call_agent_2","name":"Agent","arguments":"","status":"in_progress"}}`,
			`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_agent_2","delta":"{\"description\":\"Review tests\",\"prompt\":\"Inspect test coverage\",\"subagent_type\":\"Explore\"}"}`,
			`data: {"type":"response.function_call_arguments.done","output_index":1,"item_id":"fc_agent_2","arguments":"{\"description\":\"Review tests\",\"prompt\":\"Inspect test coverage\",\"subagent_type\":\"Explore\"}"}`,
			`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_agent_2","call_id":"call_agent_2","name":"Agent","arguments":"{\"description\":\"Review tests\",\"prompt\":\"Inspect test coverage\",\"subagent_type\":\"Explore\"}","status":"completed"}}`,
			`data: {"type":"response.completed","response":{"id":"resp-agent-stream","model":"grok-4.6","status":"completed","output":[],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}}`,
			`data: [DONE]`,
			"",
		}, "\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    req,
		}, nil
	})
	installGitHubCopilotTestTransport(t, transport)
	executor, auth := newGitHubCopilotTestExecutorAndAuth()

	payload := []byte(fmt.Sprintf(`{"model":"grok-4.6-cc","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"review"}],"tools":[{"name":"Agent","description":"Launch an agent","input_schema":%s}]}`, testClaudeAgentInputSchema))
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "grok-4.6-cc", Payload: payload}, cliproxyexecutor.Options{
		OriginalRequest: payload,
		SourceFormat:    sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var output bytes.Buffer
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}

	arguments := collectClaudeToolArguments(t, output.Bytes(), "Agent")
	if len(arguments) != 2 {
		t.Fatalf("streamed Agent argument blocks = %d, want 2 (stream=%s)", len(arguments), output.String())
	}
	for index, wantPrompt := range []string{"Inspect the change", "Inspect test coverage"} {
		if got := gjson.Get(arguments[index], "model").String(); got != "sonnet" {
			t.Fatalf("streamed Agent %d model = %q, want sonnet (arguments=%s)", index, got, arguments[index])
		}
		if got := gjson.Get(arguments[index], "prompt").String(); got != wantPrompt {
			t.Fatalf("streamed Agent %d prompt = %q, want %q (arguments=%s)", index, got, wantPrompt, arguments[index])
		}
	}
}

func collectClaudeToolArguments(t *testing.T, stream []byte, toolName string) map[int]string {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(stream))
	toolBlocks := make(map[int]bool)
	argumentBuilders := make(map[int]*strings.Builder)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		event := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
		switch event.Get("type").String() {
		case "content_block_start":
			if event.Get("content_block.type").String() == "tool_use" && event.Get("content_block.name").String() == toolName {
				index := int(event.Get("index").Int())
				toolBlocks[index] = true
				argumentBuilders[index] = &strings.Builder{}
			}
		case "content_block_delta":
			index := int(event.Get("index").Int())
			if toolBlocks[index] && event.Get("delta.type").String() == "input_json_delta" {
				argumentBuilders[index].WriteString(event.Get("delta.partial_json").String())
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan Claude stream: %v", err)
	}
	arguments := make(map[int]string, len(argumentBuilders))
	for index, builder := range argumentBuilders {
		arguments[index] = builder.String()
	}
	return arguments
}
