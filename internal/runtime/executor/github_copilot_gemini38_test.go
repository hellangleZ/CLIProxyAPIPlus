package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestGemini38ChatBridgeModel(t *testing.T) {
	claude := sdktranslator.FromString("claude")
	openAI := sdktranslator.FromString("openai")
	responses := sdktranslator.FromString("openai-response")

	tests := []struct {
		name         string
		model        string
		source       sdktranslator.Format
		wantUpstream string
		wantActive   bool
	}{
		{"gemini 3.8 flash", "gemini-3.8-flash-cc", claude, "gemini-3.8-flash", true},
		{"thinking suffix", "gemini-3.8-flash-cc(high)", claude, "gemini-3.8-flash", true},
		{"context tag and thinking suffix", "gemini-3.8-flash-cc[1m](max)", claude, "gemini-3.8-flash", true},
		{"responses handler chat fallback", "gemini-3.8-flash-cc", openAI, "gemini-3.8-flash", true},
		{"native responses source", "gemini-3.8-flash-cc", responses, "gemini-3.8-flash", true},
		{"plain gemini model", "gemini-3.8-flash", claude, "gemini-3.8-flash", false},
		{"responses bridge alias", "grok-4.6-cc", claude, "grok-4.6-cc", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream, active := gemini38ChatBridgeModel(tc.model, tc.source)
			if active != tc.wantActive {
				t.Fatalf("active = %v, want %v", active, tc.wantActive)
			}
			if upstream != tc.wantUpstream {
				t.Fatalf("upstream = %q, want %q", upstream, tc.wantUpstream)
			}
		})
	}
}

func TestGitHubCopilotGemini38NativeResponsesSourceUsesBaseChatModel(t *testing.T) {
	var capturedPaths []string
	var capturedBodies [][]byte
	transport := githubCopilotRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		capturedPaths = append(capturedPaths, req.URL.Path)
		capturedBodies = append(capturedBodies, append([]byte(nil), body...))

		if gjson.GetBytes(body, "stream").Bool() {
			response := strings.Join([]string{
				`data: {"id":"chatcmpl-gemini38-native-responses","object":"chat.completion.chunk","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"STREAM_OK"},"finish_reason":null}]}`,
				`data: {"id":"chatcmpl-gemini38-native-responses","object":"chat.completion.chunk","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":5,"total_tokens":14}}`,
				`data: [DONE]`,
				"",
			}, "\n")
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
		}

		response := `{"id":"chatcmpl-gemini38-native-responses","object":"chat.completion","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"RESPONSES_OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":5,"total_tokens":14}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})
	installGitHubCopilotTestTransport(t, transport)
	executor, auth := newGitHubCopilotTestExecutorAndAuth()

	model := "gemini-3.8-flash-cc[1m](max)"
	nonStreamPayload := []byte(`{"model":"gemini-3.8-flash-cc[1m](max)","input":"reply with the marker","max_output_tokens":128,"stream":false}`)
	nonStreamReq := cliproxyexecutor.Request{Model: model, Payload: nonStreamPayload}
	nonStreamOpts := cliproxyexecutor.Options{OriginalRequest: nonStreamPayload, SourceFormat: sdktranslator.FromString("openai-response")}
	nonStreamResp, err := executor.Execute(context.Background(), auth, nonStreamReq, nonStreamOpts)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(nonStreamResp.Payload, "object").String(); got != "response" {
		t.Fatalf("response object = %q, want response (body=%s)", got, string(nonStreamResp.Payload))
	}
	if got := gjson.GetBytes(nonStreamResp.Payload, "output.0.content.0.text").String(); got != "RESPONSES_OK" {
		t.Fatalf("response text = %q, want RESPONSES_OK (body=%s)", got, string(nonStreamResp.Payload))
	}

	streamPayload := []byte(`{"model":"gemini-3.8-flash-cc[1m](max)","input":"reply with the marker","max_output_tokens":128,"stream":true}`)
	streamReq := cliproxyexecutor.Request{Model: model, Payload: streamPayload}
	streamOpts := cliproxyexecutor.Options{OriginalRequest: streamPayload, SourceFormat: sdktranslator.FromString("openai-response")}
	stream, err := executor.ExecuteStream(context.Background(), auth, streamReq, streamOpts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var streamOutput bytes.Buffer
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		streamOutput.Write(chunk.Payload)
	}
	if !strings.Contains(streamOutput.String(), "response.output_text.delta") || !strings.Contains(streamOutput.String(), "STREAM_OK") {
		t.Fatalf("stream was not converted to Responses events: %s", streamOutput.String())
	}

	if len(capturedPaths) != 2 || len(capturedBodies) != 2 {
		t.Fatalf("captured paths/bodies = %d/%d, want 2/2", len(capturedPaths), len(capturedBodies))
	}
	for i, body := range capturedBodies {
		if capturedPaths[i] != githubCopilotChatPath {
			t.Fatalf("request %d path = %q, want %q", i, capturedPaths[i], githubCopilotChatPath)
		}
		if got := gjson.GetBytes(body, "model").String(); got != "gemini-3.8-flash" {
			t.Fatalf("request %d model = %q, want gemini-3.8-flash (body=%s)", i, got, string(body))
		}
		if gjson.GetBytes(body, "input").Exists() || !gjson.GetBytes(body, "messages").IsArray() {
			t.Fatalf("request %d was not converted to Chat Completions: %s", i, string(body))
		}
		if got := gjson.GetBytes(body, "reasoning_effort").String(); got != "high" {
			t.Fatalf("request %d reasoning_effort = %q, want high (body=%s)", i, got, string(body))
		}
	}
}

func TestGitHubCopilotGemini38ResponsesFallbackUsesBaseChatModel(t *testing.T) {
	var capturedPaths []string
	var capturedBodies [][]byte
	transport := githubCopilotRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		capturedPaths = append(capturedPaths, req.URL.Path)
		capturedBodies = append(capturedBodies, append([]byte(nil), body...))

		if gjson.GetBytes(body, "stream").Bool() {
			response := strings.Join([]string{
				`data: {"id":"chatcmpl-gemini38-responses","object":"chat.completion.chunk","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"STREAM_OK"},"finish_reason":null}]}`,
				`data: {"id":"chatcmpl-gemini38-responses","object":"chat.completion.chunk","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":5,"total_tokens":14}}`,
				`data: [DONE]`,
				"",
			}, "\n")
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
		}

		response := `{"id":"chatcmpl-gemini38-responses","object":"chat.completion","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"RESPONSES_OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":5,"total_tokens":14}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})
	installGitHubCopilotTestTransport(t, transport)
	executor, auth := newGitHubCopilotTestExecutorAndAuth()

	nonStreamPayload := []byte(`{"model":"gemini-3.8-flash-cc","messages":[{"role":"user","content":"reply with the marker"}],"stream":false}`)
	nonStreamReq := cliproxyexecutor.Request{Model: "gemini-3.8-flash-cc", Payload: nonStreamPayload}
	nonStreamOpts := cliproxyexecutor.Options{OriginalRequest: nonStreamPayload, SourceFormat: sdktranslator.FromString("openai")}
	if _, err := executor.Execute(context.Background(), auth, nonStreamReq, nonStreamOpts); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	streamPayload := []byte(`{"model":"gemini-3.8-flash-cc","messages":[{"role":"user","content":"reply with the marker"}],"stream":true}`)
	streamReq := cliproxyexecutor.Request{Model: "gemini-3.8-flash-cc", Payload: streamPayload}
	streamOpts := cliproxyexecutor.Options{OriginalRequest: streamPayload, SourceFormat: sdktranslator.FromString("openai")}
	stream, err := executor.ExecuteStream(context.Background(), auth, streamReq, streamOpts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}

	if len(capturedPaths) != 2 || len(capturedBodies) != 2 {
		t.Fatalf("captured paths/bodies = %d/%d, want 2/2", len(capturedPaths), len(capturedBodies))
	}
	for i, body := range capturedBodies {
		if capturedPaths[i] != githubCopilotChatPath {
			t.Fatalf("request %d path = %q, want %q", i, capturedPaths[i], githubCopilotChatPath)
		}
		if got := gjson.GetBytes(body, "model").String(); got != "gemini-3.8-flash" {
			t.Fatalf("request %d model = %q, want gemini-3.8-flash (body=%s)", i, got, string(body))
		}
		if gjson.GetBytes(body, "input").Exists() {
			t.Fatalf("request %d unexpectedly used Responses input: %s", i, string(body))
		}
	}
}

func TestNormalizeGemini38ReasoningResponse(t *testing.T) {
	nonStream := []byte(`{"choices":[{"message":{"reasoning_text":"visible thought","reasoning_opaque":"opaque-token","content":"answer"}}]}`)
	out := normalizeGemini38ReasoningResponse(nonStream, "gemini-3.8-flash-cc[1m](high)")
	if got := gjson.GetBytes(out, "choices.0.message.reasoning_content").String(); got != "visible thought" {
		t.Fatalf("reasoning_content = %q, want visible thought (body=%s)", got, string(out))
	}
	if got := gjson.GetBytes(out, "choices.0.message.reasoning_opaque").String(); got != "opaque-token" {
		t.Fatalf("reasoning_opaque changed: %q (body=%s)", got, string(out))
	}

	stream := []byte(`data: {"choices":[{"delta":{"reasoning_text":"streamed thought"}}]}`)
	streamOut := normalizeGemini38ReasoningResponse(stream, "gemini-3.8-flash-cc")
	streamJSON := bytes.TrimSpace(bytes.TrimPrefix(streamOut, []byte("data:")))
	if got := gjson.GetBytes(streamJSON, "choices.0.delta.reasoning_content").String(); got != "streamed thought" {
		t.Fatalf("stream reasoning_content = %q, want streamed thought (line=%s)", got, string(streamOut))
	}

	withoutReasoning := []byte(`data: {"choices":[{"delta":{"content":"answer"}}]}`)
	if got := normalizeGemini38ReasoningResponse(withoutReasoning, "gemini-3.8-flash-cc"); string(got) != string(withoutReasoning) {
		t.Fatalf("SSE without reasoning changed: %s", string(got))
	}

	alreadyNormalized := []byte(`{"choices":[{"message":{"reasoning_text":"new","reasoning_content":"existing"}}]}`)
	if got := normalizeGemini38ReasoningResponse(alreadyNormalized, "gemini-3.8-flash-cc"); gjson.GetBytes(got, "choices.0.message.reasoning_content").String() != "existing" {
		t.Fatalf("existing reasoning_content was overwritten: %s", string(got))
	}

	if got := normalizeGemini38ReasoningResponse(nonStream, "gpt-5.6-sol-cc"); string(got) != string(nonStream) {
		t.Fatalf("non-Gemini response changed: %s", string(got))
	}
}

func TestNormalizeGemini38RequestEffortHonorsMaxSuffix(t *testing.T) {
	body := []byte(`{"reasoning_effort":"low"}`)
	out := normalizeGemini38RequestEffort(body, "gemini-3.8-flash-cc[1m](max)")
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want high (body=%s)", got, string(out))
	}

	unchanged := normalizeGemini38RequestEffort(body, "gemini-3.8-flash-cc")
	if got := gjson.GetBytes(unchanged, "reasoning_effort").String(); got != "low" {
		t.Fatalf("unsuffixed low effort changed to %q (body=%s)", got, string(unchanged))
	}

	auto := normalizeGemini38RequestEffort(body, "gemini-3.8-flash-cc(auto)")
	if gjson.GetBytes(auto, "reasoning_effort").Exists() {
		t.Fatalf("auto suffix left an explicit effort: %s", string(auto))
	}
}

func TestGitHubCopilotExecutePathsForceTextForGemini38Compact(t *testing.T) {
	var capturedPaths []string
	var capturedBodies [][]byte
	transport := githubCopilotRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		capturedPaths = append(capturedPaths, req.URL.Path)
		capturedBodies = append(capturedBodies, append([]byte(nil), body...))
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(geminiPromptLimitErrorBody)),
			Request:    req,
		}, nil
	})
	installGitHubCopilotTestTransport(t, transport)
	executor, auth := newGitHubCopilotTestExecutorAndAuth()

	payload := []byte(fmt.Sprintf(`{"model":"gemini-3.8-flash-cc","max_tokens":32000,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"messages":[{"role":"user","content":"old context"},{"role":"user","content":[{"type":"text","text":%q}]},{"role":"system","content":"token budget"}],"tools":[{"name":"Bash","description":"Run a command","input_schema":{"type":"object"}}]}`, claudeCodeCompactPromptText))
	req := cliproxyexecutor.Request{Model: "gemini-3.8-flash-cc", Payload: payload}
	opts := cliproxyexecutor.Options{OriginalRequest: payload, SourceFormat: sdktranslator.FromString("claude")}

	_, nonStreamErr := executor.Execute(context.Background(), auth, req, opts)
	if nonStreamErr == nil || nonStreamErr.Error() != standardPromptTooLongErrorText {
		t.Fatalf("non-streaming error = %v, want %q", nonStreamErr, standardPromptTooLongErrorText)
	}
	stream, streamErr := executor.ExecuteStream(context.Background(), auth, req, opts)
	if stream != nil || streamErr == nil || streamErr.Error() != standardPromptTooLongErrorText {
		t.Fatalf("streaming bootstrap = (%v, %v), want nil stream and %q", stream, streamErr, standardPromptTooLongErrorText)
	}

	if len(capturedBodies) != 2 || len(capturedPaths) != 2 {
		t.Fatalf("captured paths/bodies = %d/%d, want 2/2", len(capturedPaths), len(capturedBodies))
	}
	for i, body := range capturedBodies {
		if capturedPaths[i] != githubCopilotChatPath {
			t.Fatalf("request %d path = %q, want %q", i, capturedPaths[i], githubCopilotChatPath)
		}
		if got := gjson.GetBytes(body, "model").String(); got != "gemini-3.8-flash" {
			t.Fatalf("request %d model = %q, want gemini-3.8-flash", i, got)
		}
		if got := gjson.GetBytes(body, "tool_choice").String(); got != "none" {
			t.Fatalf("request %d tool_choice = %q, want none (body=%s)", i, got, string(body))
		}
		if got := gjson.GetBytes(body, "tools.#").Int(); got != 1 {
			t.Fatalf("request %d tools count = %d, want 1", i, got)
		}
		if got := gjson.GetBytes(body, "reasoning_effort").String(); got != "high" {
			t.Fatalf("request %d reasoning effort = %q, want high", i, got)
		}
	}
}

func TestGitHubCopilotGemini38ClaudeBridgeUsesChatCompletions(t *testing.T) {
	var capturedPath string
	var capturedBody []byte
	var capturedIntegration string
	transport := githubCopilotRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedPath = req.URL.Path
		capturedIntegration = req.Header.Get("Copilot-Integration-Id")
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		capturedBody = append([]byte(nil), body...)
		response := `{"id":"chatcmpl-gemini38","object":"chat.completion","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","reasoning_text":"visible thought","reasoning_opaque":"opaque-token","content":"GEMINI38_OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})
	installGitHubCopilotTestTransport(t, transport)
	executor, auth := newGitHubCopilotTestExecutorAndAuth()

	payload := []byte(`{"model":"gemini-3.8-flash-cc","max_tokens":256,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"messages":[{"role":"user","content":"reply with the marker"}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "gemini-3.8-flash-cc", Payload: payload}, cliproxyexecutor.Options{OriginalRequest: payload, SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if capturedPath != githubCopilotChatPath {
		t.Fatalf("upstream path = %q, want %q", capturedPath, githubCopilotChatPath)
	}
	if capturedIntegration != copilotIntegrationID {
		t.Fatalf("integration = %q, want %q", capturedIntegration, copilotIntegrationID)
	}
	if got := gjson.GetBytes(capturedBody, "model").String(); got != "gemini-3.8-flash" {
		t.Fatalf("upstream model = %q, want gemini-3.8-flash (body=%s)", got, string(capturedBody))
	}
	if gjson.GetBytes(capturedBody, "input").Exists() {
		t.Fatalf("Gemini chat request unexpectedly used Responses input: %s", string(capturedBody))
	}
	if got := gjson.GetBytes(capturedBody, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want high (body=%s)", got, string(capturedBody))
	}
	if got := gjson.GetBytes(resp.Payload, `content.#(type=="thinking").thinking`).String(); got != "visible thought" {
		t.Fatalf("thinking = %q, want visible thought (body=%s)", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, `content.#(type=="text").text`).String(); got != "GEMINI38_OK" {
		t.Fatalf("text = %q, want GEMINI38_OK (body=%s)", got, string(resp.Payload))
	}
}

func TestGitHubCopilotGemini38ClaudeBridgePreservesToolCalls(t *testing.T) {
	var capturedBody []byte
	transport := githubCopilotRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		capturedBody = append([]byte(nil), body...)
		response := `{"id":"chatcmpl-gemini38-tool","object":"chat.completion","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","reasoning_text":"I should use the requested tool.","tool_calls":[{"id":"call_city_1","type":"function","function":{"name":"lookup_city","arguments":"{\"city\":\"Paris\"}"}}],"content":""},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":21,"completion_tokens":12,"total_tokens":33}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})
	installGitHubCopilotTestTransport(t, transport)
	executor, auth := newGitHubCopilotTestExecutorAndAuth()

	payload := []byte(`{"model":"gemini-3.8-flash-cc","max_tokens":256,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"Look up Paris"}],"tools":[{"name":"lookup_city","description":"Look up a city","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}],"tool_choice":{"type":"tool","name":"lookup_city"}}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "gemini-3.8-flash-cc", Payload: payload}, cliproxyexecutor.Options{OriginalRequest: payload, SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(capturedBody, "tools.0.function.name").String(); got != "lookup_city" {
		t.Fatalf("upstream tool name = %q, want lookup_city (body=%s)", got, string(capturedBody))
	}
	if got := gjson.GetBytes(capturedBody, "tool_choice.function.name").String(); got != "lookup_city" {
		t.Fatalf("upstream forced tool = %q, want lookup_city (body=%s)", got, string(capturedBody))
	}
	if got := gjson.GetBytes(resp.Payload, `content.#(type=="tool_use").id`).String(); got != "call_city_1" {
		t.Fatalf("Claude tool ID = %q, want call_city_1 (body=%s)", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, `content.#(type=="tool_use").name`).String(); got != "lookup_city" {
		t.Fatalf("Claude tool name = %q, want lookup_city (body=%s)", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, `content.#(type=="tool_use").input.city`).String(); got != "Paris" {
		t.Fatalf("Claude tool city = %q, want Paris (body=%s)", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "stop_reason").String(); got != "tool_use" {
		t.Fatalf("stop reason = %q, want tool_use (body=%s)", got, string(resp.Payload))
	}
}

func TestGitHubCopilotGemini38ClaudeBridgeStreamsThinkingAndText(t *testing.T) {
	var capturedPath string
	var capturedBody []byte
	transport := githubCopilotRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedPath = req.URL.Path
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		capturedBody = append([]byte(nil), body...)
		response := strings.Join([]string{
			`data: {"id":"chatcmpl-gemini38","object":"chat.completion.chunk","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"role":"assistant","reasoning_text":"stream thought"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-gemini38","object":"chat.completion.chunk","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"content":"STREAM_OK"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-gemini38","object":"chat.completion.chunk","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":5,"total_tokens":14}}`,
			`data: [DONE]`,
			"",
		}, "\n")
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})
	installGitHubCopilotTestTransport(t, transport)
	executor, auth := newGitHubCopilotTestExecutorAndAuth()

	payload := []byte(`{"model":"gemini-3.8-flash-cc","max_tokens":256,"stream":true,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"reply with the marker"}]}`)
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "gemini-3.8-flash-cc", Payload: payload}, cliproxyexecutor.Options{OriginalRequest: payload, SourceFormat: sdktranslator.FromString("claude")})
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

	if capturedPath != githubCopilotChatPath {
		t.Fatalf("upstream path = %q, want %q", capturedPath, githubCopilotChatPath)
	}
	if got := gjson.GetBytes(capturedBody, "model").String(); got != "gemini-3.8-flash" {
		t.Fatalf("upstream model = %q, want gemini-3.8-flash (body=%s)", got, string(capturedBody))
	}
	if !gjson.GetBytes(capturedBody, "stream").Bool() || !gjson.GetBytes(capturedBody, "stream_options.include_usage").Bool() {
		t.Fatalf("stream flags missing from upstream body: %s", string(capturedBody))
	}
	streamOutput := output.String()
	if !strings.Contains(streamOutput, `"type":"thinking_delta","thinking":"stream thought"`) {
		t.Fatalf("Claude stream missing thinking delta: %s", streamOutput)
	}
	if !strings.Contains(streamOutput, `"type":"text_delta","text":"STREAM_OK"`) {
		t.Fatalf("Claude stream missing text delta: %s", streamOutput)
	}
	if strings.Contains(streamOutput, "reasoning_text") {
		t.Fatalf("raw Gemini reasoning field leaked into Claude stream: %s", streamOutput)
	}
}
