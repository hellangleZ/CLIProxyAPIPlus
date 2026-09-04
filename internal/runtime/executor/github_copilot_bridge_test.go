package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

type githubCopilotRoundTripFunc func(*http.Request) (*http.Response, error)

const (
	grokPromptLimitErrorBody       = `{"error":{"code":"invalid_request_body","message":"{\"code\":\"invalid-argument\",\"error\":\"This model's maximum prompt length is 500000 but the request contains 500271 tokens.\"}"}}`
	solPromptLimitErrorBody        = `{"error":{"code":"invalid_request_body","message":"{\"code\":\"invalid-argument\",\"error\":\"This model's maximum prompt length is 1000000 but the request contains 1000271 tokens.\"}"}}`
	solContextWindowErrorBody      = `{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","code":"invalid_request_body"}}`
	geminiPromptLimitErrorBody     = `{"error":{"message":"invalid request body","code":"invalid_request_body"}}`
	standardPromptTooLongErrorText = "prompt is too long"
	claudeCodeCompactPromptText    = "CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\nYour task is to create a detailed summary of the conversation so far."
)

func (f githubCopilotRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestClaudeResponsesBridgeModel(t *testing.T) {
	claude := sdktranslator.FromString("claude")
	responses := sdktranslator.FromString("openai-response")

	cases := []struct {
		name         string
		model        string
		source       sdktranslator.Format
		wantUpstream string
		wantActive   bool
	}{
		{"sol bridge", "gpt-5.6-sol-cc", claude, "gpt-5.6-sol", true},
		{"luna bridge", "gpt-5.6-luna-cc", claude, "gpt-5.6-luna", true},
		{"terra bridge", "gpt-5.6-terra-cc", claude, "gpt-5.6-terra", true},
		{"gpt 5.5 bridge", "gpt-5.5-cc", claude, "gpt-5.5", true},
		{"grok 4.5 bridge", "grok-4.5-cc", claude, "grok-4.5", true},
		{"grok 4.6 bridge", "grok-4.6-cc", claude, "grok-4.6", true},
		{"gemini chat alias is not a responses bridge", "gemini-3.8-flash-cc", claude, "gemini-3.8-flash-cc", false},
		{"bridge with thinking suffix", "gpt-5.6-sol-cc(high)", claude, "gpt-5.6-sol", true},
		{"bridge with context tag", "gpt-5.6-sol-cc[1m]", claude, "gpt-5.6-sol", true},
		{"non-claude source untouched", "gpt-5.6-sol-cc", responses, "gpt-5.6-sol-cc", false},
		{"plain gpt untouched", "gpt-5.6-sol", claude, "gpt-5.6-sol", false},
		{"claude model untouched", "claude-opus-4.8", claude, "claude-opus-4.8", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream, active := claudeResponsesBridgeModel(tc.model, tc.source)
			if active != tc.wantActive {
				t.Fatalf("active = %v, want %v", active, tc.wantActive)
			}
			if upstream != tc.wantUpstream {
				t.Fatalf("upstream = %q, want %q", upstream, tc.wantUpstream)
			}
		})
	}
}

func TestRewriteBridgeModel(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol-cc","input":[]}`)
	out := rewriteBridgeModel(body, "gpt-5.6-sol")
	if got := gjson.GetBytes(out, "model").String(); got != "gpt-5.6-sol" {
		t.Fatalf("model = %q, want gpt-5.6-sol", got)
	}

	// Empty upstream is a no-op.
	unchanged := rewriteBridgeModel(body, "")
	if got := gjson.GetBytes(unchanged, "model").String(); got != "gpt-5.6-sol-cc" {
		t.Fatalf("model = %q, want gpt-5.6-sol-cc (unchanged)", got)
	}
}

func TestRepairResponsesInputCallIDsPairsEmptyToolHistory(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":[]},{"type":"function_call","id":"fc_1","call_id":"","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"","output":"ok"}]}`)

	out := repairResponsesInputCallIDs(body)

	callID := gjson.GetBytes(out, "input.1.call_id").String()
	outputCallID := gjson.GetBytes(out, "input.2.call_id").String()
	if callID == "" {
		t.Fatalf("function_call call_id is still empty: %s", string(out))
	}
	if outputCallID != callID {
		t.Fatalf("function_call_output call_id = %q, want paired %q (body=%s)", outputCallID, callID, string(out))
	}
}

func TestRepairResponsesInputCallIDsRepairsOrphanOutput(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call_output","call_id":"","output":"ok"}]}`)

	out := repairResponsesInputCallIDs(body)

	if got := gjson.GetBytes(out, "input.0.call_id").String(); got != "call_output_0" {
		t.Fatalf("orphan function_call_output call_id = %q, want call_output_0 (body=%s)", got, string(out))
	}
}

func TestNormalizeCopilotEffortForModel(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		body       string
		path       string
		wantEffort string
	}{
		{"gpt 5.5 max", "gpt-5.5-cc", `{"reasoning":{"effort":"max"}}`, "reasoning.effort", "xhigh"},
		{"grok 4.5 max", "grok-4.5-cc", `{"reasoning":{"effort":"max"}}`, "reasoning.effort", "xhigh"},
		{"grok 4.6 max", "grok-4.6-cc", `{"reasoning":{"effort":"max"}}`, "reasoning.effort", "xhigh"},
		{"grok 4.5 none omitted", "grok-4.5-cc", `{"reasoning":{"effort":"none","summary":"auto"}}`, "reasoning", ""},
		{"grok 4.5 chat none omitted", "grok-4.5-cc", `{"reasoning_effort":"none"}`, "reasoning_effort", ""},
		{"grok 4.6 none omitted", "grok-4.6-cc", `{"reasoning":{"effort":"none","summary":"auto"}}`, "reasoning", ""},
		{"grok 4.6 chat none omitted", "grok-4.6-cc", `{"reasoning_effort":"none"}`, "reasoning_effort", ""},
		{"gpt 5.5 none unchanged", "gpt-5.5-cc", `{"reasoning":{"effort":"none"}}`, "reasoning.effort", "none"},
		{"gpt 5.6 none unchanged", "gpt-5.6-sol-cc", `{"reasoning":{"effort":"none"}}`, "reasoning.effort", "none"},
		{"thinking suffix", "gpt-5.5-cc(high)", `{"reasoning":{"effort":"max"}}`, "reasoning.effort", "xhigh"},
		{"context tag", "gpt-5.5-cc[1m]", `{"reasoning":{"effort":"max"}}`, "reasoning.effort", "xhigh"},
		{"gpt 5.6 max", "gpt-5.6-sol-cc", `{"reasoning":{"effort":"max"}}`, "reasoning.effort", "max"},
		{"chat schema", "gpt-5.5-cc", `{"reasoning_effort":"max"}`, "reasoning_effort", "xhigh"},
		{"gemini max maps to high", "gemini-3.8-flash-cc", `{"reasoning_effort":"max"}`, "reasoning_effort", "high"},
		{"gemini xhigh maps to high", "gemini-3.8-flash-cc[1m](xhigh)", `{"reasoning_effort":"xhigh"}`, "reasoning_effort", "high"},
		{"gemini none maps to low", "gemini-3.8-flash-cc", `{"reasoning_effort":"none"}`, "reasoning_effort", "low"},
		{"plain gemini max unchanged", "gemini-3.8-flash", `{"reasoning_effort":"max"}`, "reasoning_effort", "max"},
		{"high unchanged", "gpt-5.5-cc", `{"reasoning":{"effort":"high"}}`, "reasoning.effort", "high"},
		{"ultra remains global max", "gpt-5.6-sol-cc", `{"reasoning":{"effort":"ultra"}}`, "reasoning.effort", "max"},
		{"gemini ultra maps to high", "gemini-3.8-flash-cc", `{"reasoning_effort":"ultra"}`, "reasoning_effort", "high"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := normalizeCopilotEffort([]byte(tc.body), tc.model)
			if got := gjson.GetBytes(out, tc.path).String(); got != tc.wantEffort {
				t.Fatalf("effort = %q, want %q (body=%s)", got, tc.wantEffort, string(out))
			}
		})
	}
}

func TestForceTextForClaudeCodeCompactRequest(t *testing.T) {
	originalPayload := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol-cc","messages":[{"role":"user","content":"old context"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"done"},{"type":"text","text":%q}]},{"role":"system","content":"token budget"}],"tools":[{"name":"Bash"}]}`, claudeCodeCompactPromptText))
	translatedBody := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":[]}],"tools":[{"type":"function","name":"Bash"}],"tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"max","summary":"auto"}}`)

	out := forceTextForClaudeCodeCompactRequest(translatedBody, originalPayload, "gpt-5.6-sol-cc[1m](max)")

	if got := gjson.GetBytes(out, "tool_choice").String(); got != "none" {
		t.Fatalf("tool_choice = %q, want none (body=%s)", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tools count = %d, want 1 (body=%s)", got, string(out))
	}
	if !gjson.GetBytes(out, "parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls changed: %s", string(out))
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "max" {
		t.Fatalf("reasoning effort = %q, want max (body=%s)", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.role").String(); got != "user" {
		t.Fatalf("input history changed: %s", string(out))
	}

	withoutTools := []byte(`{"model":"gpt-5.6-sol","reasoning":{"effort":"max"}}`)
	if got := forceTextForClaudeCodeCompactRequest(withoutTools, originalPayload, "gpt-5.6-sol-cc"); string(got) != string(withoutTools) {
		t.Fatalf("compact request without tools changed unexpectedly: %s", string(got))
	}

	geminiBody := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"old context"}],"tools":[{"type":"function","function":{"name":"Bash"}}],"tool_choice":"auto","parallel_tool_calls":true,"reasoning_effort":"high"}`)
	geminiOut := forceTextForClaudeCodeCompactRequest(geminiBody, originalPayload, "gemini-3.8-flash-cc[1m](high)")
	if got := gjson.GetBytes(geminiOut, "tool_choice").String(); got != "none" {
		t.Fatalf("Gemini tool_choice = %q, want none (body=%s)", got, string(geminiOut))
	}
	if got := gjson.GetBytes(geminiOut, "tools.#").Int(); got != 1 {
		t.Fatalf("Gemini tools count = %d, want 1 (body=%s)", got, string(geminiOut))
	}
	if got := gjson.GetBytes(geminiOut, "reasoning_effort").String(); got != "high" {
		t.Fatalf("Gemini reasoning effort = %q, want high (body=%s)", got, string(geminiOut))
	}
}

func TestForceTextForClaudeCodeCompactRequestLeavesOtherRequestsUntouched(t *testing.T) {
	translatedBody := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"function","name":"Bash"}],"tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"max"}}`)
	compactPayload := fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"text","text":%q}]}]}`, claudeCodeCompactPromptText)

	tests := []struct {
		name    string
		model   string
		payload string
	}{
		{
			name:    "ordinary sol request",
			model:   "gpt-5.6-sol-cc",
			payload: `{"messages":[{"role":"user","content":"continue"}]}`,
		},
		{
			name:    "ordinary gemini request",
			model:   "gemini-3.8-flash-cc",
			payload: `{"messages":[{"role":"user","content":"continue"}]}`,
		},
		{
			name:  "compact marker only in old history",
			model: "gpt-5.6-sol-cc",
			payload: fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"text","text":%q}]},{"role":"assistant","content":"summary"},{"role":"user","content":"continue"},{"role":"system","content":"token budget"}]}`,
				claudeCodeCompactPromptText),
		},
		{
			name:    "gpt luna compact request",
			model:   "gpt-5.6-luna-cc",
			payload: compactPayload,
		},
		{
			name:    "grok compact request remains unchanged",
			model:   "grok-4.6-cc",
			payload: compactPayload,
		},
		{
			name:    "plain sol compact request",
			model:   "gpt-5.6-sol",
			payload: compactPayload,
		},
		{
			name:    "malformed request",
			model:   "gpt-5.6-sol-cc",
			payload: `{"messages":`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := forceTextForClaudeCodeCompactRequest(translatedBody, []byte(tc.payload), tc.model)
			if string(out) != string(translatedBody) {
				t.Fatalf("request changed unexpectedly: %s", string(out))
			}
		})
	}
}

func TestNewGitHubCopilotStatusErrNormalizesSupportedPromptLimits(t *testing.T) {
	tests := []struct {
		name  string
		model string
		body  string
		want  string
	}{
		{
			name:  "grok 4.5",
			model: "grok-4.5-cc",
			body:  grokPromptLimitErrorBody,
			want:  "prompt is too long: 500271 tokens > 500000",
		},
		{
			name:  "grok 4.6 with suffixes",
			model: "grok-4.6-cc[1m](high)",
			body:  grokPromptLimitErrorBody,
			want:  "prompt is too long: 500271 tokens > 500000",
		},
		{
			name:  "gpt 5.6 sol nested limit",
			model: "gpt-5.6-sol-cc",
			body:  solPromptLimitErrorBody,
			want:  "prompt is too long: 1000271 tokens > 1000000",
		},
		{
			name:  "gpt 5.6 sol direct context error",
			model: "gpt-5.6-sol-cc",
			body:  solContextWindowErrorBody,
			want:  standardPromptTooLongErrorText,
		},
		{
			name:  "gpt 5.6 sol direct context error with suffixes",
			model: "gpt-5.6-sol-cc[1m](max)",
			body:  solContextWindowErrorBody,
			want:  standardPromptTooLongErrorText,
		},
		{
			name:  "gemini 3.8 flash direct context error",
			model: "gemini-3.8-flash-cc",
			body:  geminiPromptLimitErrorBody,
			want:  standardPromptTooLongErrorText,
		},
		{
			name:  "gemini 3.8 flash direct context error with suffixes",
			model: "gemini-3.8-flash-cc[1m](high)",
			body:  geminiPromptLimitErrorBody,
			want:  standardPromptTooLongErrorText,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := newGitHubCopilotStatusErr(http.StatusBadRequest, []byte(tc.body), tc.model)
			if err.code != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d", err.code, http.StatusBadRequest)
			}
			if err.msg != tc.want {
				t.Fatalf("message = %q, want %q", err.msg, tc.want)
			}
		})
	}
}

func TestNewGitHubCopilotStatusErrNormalizesGemini38PayloadLimit(t *testing.T) {
	err := newGitHubCopilotStatusErr(http.StatusRequestEntityTooLarge, []byte("Request Entity Too Large"), "gemini-3.8-flash-cc[1m](high)")
	if err.code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", err.code, http.StatusBadRequest)
	}
	if err.msg != standardPromptTooLongErrorText {
		t.Fatalf("message = %q, want %q", err.msg, standardPromptTooLongErrorText)
	}
}

func TestNewGitHubCopilotStatusErrLeavesUnrelatedErrorsUntouched(t *testing.T) {
	contextLimitBody := []byte(grokPromptLimitErrorBody)

	tests := []struct {
		name   string
		status int
		model  string
		body   []byte
	}{
		{
			name:   "gpt luna bridge alias",
			status: http.StatusBadRequest,
			model:  "gpt-5.6-luna-cc",
			body:   []byte(solContextWindowErrorBody),
		},
		{
			name:   "gpt terra bridge alias",
			status: http.StatusBadRequest,
			model:  "gpt-5.6-terra-cc",
			body:   []byte(solContextWindowErrorBody),
		},
		{
			name:   "plain gpt sol model",
			status: http.StatusBadRequest,
			model:  "gpt-5.6-sol",
			body:   []byte(solContextWindowErrorBody),
		},
		{
			name:   "plain grok model",
			status: http.StatusBadRequest,
			model:  "grok-4.6",
			body:   contextLimitBody,
		},
		{
			name:   "plain gemini model",
			status: http.StatusBadRequest,
			model:  "gemini-3.8-flash",
			body:   []byte(geminiPromptLimitErrorBody),
		},
		{
			name:   "different gemini invalid request",
			status: http.StatusBadRequest,
			model:  "gemini-3.8-flash-cc",
			body:   []byte(`{"error":{"message":"invalid tool schema","code":"invalid_request_body"}}`),
		},
		{
			name:   "non bad request status",
			status: http.StatusTooManyRequests,
			model:  "grok-4.6-cc",
			body:   contextLimitBody,
		},
		{
			name:   "payload limit on plain gemini model",
			status: http.StatusRequestEntityTooLarge,
			model:  "gemini-3.8-flash",
			body:   []byte("Request Entity Too Large"),
		},
		{
			name:   "different payload limit body on gemini alias",
			status: http.StatusRequestEntityTooLarge,
			model:  "gemini-3.8-flash-cc",
			body:   []byte("payload too large"),
		},
		{
			name:   "ordinary bad request",
			status: http.StatusBadRequest,
			model:  "grok-4.6-cc",
			body:   []byte(`{"error":{"code":"invalid_request_body","message":"unsupported parameter"}}`),
		},
		{
			name:   "malformed outer json",
			status: http.StatusBadRequest,
			model:  "grok-4.6-cc",
			body:   []byte(`{"error":`),
		},
		{
			name:   "malformed nested json",
			status: http.StatusBadRequest,
			model:  "grok-4.6-cc",
			body:   []byte(`{"error":{"code":"invalid_request_body","message":"not-json"}}`),
		},
		{
			name:   "different nested error code",
			status: http.StatusBadRequest,
			model:  "grok-4.6-cc",
			body:   []byte(`{"error":{"code":"invalid_request_body","message":"{\"code\":\"other\",\"error\":\"This model's maximum prompt length is 500000 but the request contains 500271 tokens.\"}"}}`),
		},
		{
			name:   "request does not exceed limit",
			status: http.StatusBadRequest,
			model:  "grok-4.6-cc",
			body:   []byte(`{"error":{"code":"invalid_request_body","message":"{\"code\":\"invalid-argument\",\"error\":\"This model's maximum prompt length is 500000 but the request contains 500000 tokens.\"}"}}`),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := newGitHubCopilotStatusErr(tc.status, tc.body, tc.model)
			if err.code != tc.status {
				t.Fatalf("status code = %d, want %d", err.code, tc.status)
			}
			if err.msg != string(tc.body) {
				t.Fatalf("message = %q, want original %q", err.msg, string(tc.body))
			}
		})
	}
}

func TestGitHubCopilotExecutePathsNormalizePromptLimit(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		upstreamStatus int
		upstreamBody   string
		wantStatus     int
		want           string
	}{
		{
			name:           "grok 4.6",
			model:          "grok-4.6-cc",
			upstreamStatus: http.StatusBadRequest,
			upstreamBody:   grokPromptLimitErrorBody,
			wantStatus:     http.StatusBadRequest,
			want:           "prompt is too long: 500271 tokens > 500000",
		},
		{
			name:           "gpt 5.6 sol",
			model:          "gpt-5.6-sol-cc",
			upstreamStatus: http.StatusBadRequest,
			upstreamBody:   solContextWindowErrorBody,
			wantStatus:     http.StatusBadRequest,
			want:           standardPromptTooLongErrorText,
		},
		{
			name:           "gemini 3.8 flash context limit",
			model:          "gemini-3.8-flash-cc",
			upstreamStatus: http.StatusBadRequest,
			upstreamBody:   geminiPromptLimitErrorBody,
			wantStatus:     http.StatusBadRequest,
			want:           standardPromptTooLongErrorText,
		},
		{
			name:           "gemini 3.8 flash payload limit",
			model:          "gemini-3.8-flash-cc[1m](high)",
			upstreamStatus: http.StatusRequestEntityTooLarge,
			upstreamBody:   copilotPayloadTooLargeMessage,
			wantStatus:     http.StatusBadRequest,
			want:           standardPromptTooLongErrorText,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testGitHubCopilotExecutePathsNormalizePromptLimit(t, tc.model, tc.upstreamStatus, tc.upstreamBody, tc.wantStatus, tc.want)
		})
	}
}

func testGitHubCopilotExecutePathsNormalizePromptLimit(t *testing.T, model string, upstreamStatus int, upstreamBody string, wantStatus int, want string) {
	t.Helper()
	transport := githubCopilotRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: upstreamStatus,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
			Request:    req,
		}, nil
	})
	installGitHubCopilotTestTransport(t, transport)
	executor, auth := newGitHubCopilotTestExecutorAndAuth()

	payload := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, model))
	req := cliproxyexecutor.Request{Model: model, Payload: payload}
	opts := cliproxyexecutor.Options{
		OriginalRequest: payload,
		SourceFormat:    sdktranslator.FromString("claude"),
	}

	tests := []struct {
		name string
		call func(*testing.T) error
	}{
		{
			name: "non-streaming",
			call: func(_ *testing.T) error {
				_, err := executor.Execute(context.Background(), auth, req, opts)
				return err
			},
		},
		{
			name: "streaming bootstrap",
			call: func(t *testing.T) error {
				stream, err := executor.ExecuteStream(context.Background(), auth, req, opts)
				if stream != nil {
					t.Fatalf("streaming bootstrap returned a stream for upstream status %d", upstreamStatus)
				}
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(t)
			if err == nil {
				t.Fatal("expected upstream error")
			}
			if got := err.Error(); got != want {
				t.Fatalf("error = %q, want %q", got, want)
			}
			statusErr, ok := err.(interface{ StatusCode() int })
			if !ok || statusErr.StatusCode() != wantStatus {
				t.Fatalf("status = %v, want %d", statusErr, wantStatus)
			}
		})
	}
}

func TestGitHubCopilotExecutePathsForceTextForSolCompact(t *testing.T) {
	var capturedBodies [][]byte
	transport := githubCopilotRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		capturedBodies = append(capturedBodies, append([]byte(nil), body...))
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(solContextWindowErrorBody)),
			Request:    req,
		}, nil
	})
	installGitHubCopilotTestTransport(t, transport)
	executor, auth := newGitHubCopilotTestExecutorAndAuth()

	payload := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol-cc","max_tokens":32000,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"messages":[{"role":"user","content":"old context"},{"role":"user","content":[{"type":"text","text":%q}]},{"role":"system","content":"token budget"}],"tools":[{"name":"Bash","description":"Run a command","input_schema":{"type":"object"}}]}`, claudeCodeCompactPromptText))
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol-cc", Payload: payload}
	opts := cliproxyexecutor.Options{
		OriginalRequest: payload,
		SourceFormat:    sdktranslator.FromString("claude"),
	}

	_, nonStreamErr := executor.Execute(context.Background(), auth, req, opts)
	if nonStreamErr == nil {
		t.Fatal("non-streaming request unexpectedly succeeded")
	}
	stream, streamErr := executor.ExecuteStream(context.Background(), auth, req, opts)
	if stream != nil || streamErr == nil {
		t.Fatalf("streaming bootstrap = (%v, %v), want nil stream and error", stream, streamErr)
	}

	if len(capturedBodies) != 2 {
		t.Fatalf("captured %d upstream requests, want 2", len(capturedBodies))
	}
	for i, body := range capturedBodies {
		if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.6-sol" {
			t.Fatalf("request %d model = %q, want gpt-5.6-sol", i, got)
		}
		if got := gjson.GetBytes(body, "tool_choice").String(); got != "none" {
			t.Fatalf("request %d tool_choice = %q, want none (body=%s)", i, got, string(body))
		}
		if got := gjson.GetBytes(body, "tools.#").Int(); got != 1 {
			t.Fatalf("request %d tools count = %d, want 1", i, got)
		}
	}
}

func installGitHubCopilotTestTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()

	httpClientCacheMutex.Lock()
	previousClient, hadPreviousClient := httpClientCache[""]
	httpClientCache[""] = &http.Client{Transport: transport}
	httpClientCacheMutex.Unlock()
	t.Cleanup(func() {
		httpClientCacheMutex.Lock()
		defer httpClientCacheMutex.Unlock()
		if hadPreviousClient {
			httpClientCache[""] = previousClient
		} else {
			delete(httpClientCache, "")
		}
	})
}

func newGitHubCopilotTestExecutorAndAuth() (*GitHubCopilotExecutor, *cliproxyauth.Auth) {
	const accessToken = "test-github-access-token"
	executor := NewGitHubCopilotExecutor(nil)
	executor.cache[accessToken] = &cachedAPIToken{
		token:     "test-copilot-api-token",
		expiresAt: time.Now().Add(time.Hour),
	}
	auth := &cliproxyauth.Auth{
		ID:       "test-github-copilot-auth",
		Metadata: map[string]any{"access_token": accessToken},
	}
	return executor, auth
}

func TestGitHubCopilotCountTokensClaude(t *testing.T) {
	executor := NewGitHubCopilotExecutor(nil)
	payload := []byte(`{"model":"grok-4.5-cc","system":"Be concise","messages":[{"role":"user","content":[{"type":"text","text":"hello world"}]}],"tools":[{"name":"lookup","description":"Look up a value","input_schema":{"type":"object","properties":{"key":{"type":"string"}}}}]}`)

	resp, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "grok-4.5-cc",
		Payload: payload,
	}, cliproxyexecutor.Options{
		OriginalRequest: payload,
		SourceFormat:    sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}
	if !gjson.ValidBytes(resp.Payload) {
		t.Fatalf("invalid JSON response: %s", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "input_tokens").Int(); got <= 0 {
		t.Fatalf("input_tokens = %d, want positive count (body=%s)", got, string(resp.Payload))
	}
	if gjson.GetBytes(resp.Payload, "usage").Exists() {
		t.Fatalf("Claude count response should not contain OpenAI usage wrapper: %s", string(resp.Payload))
	}
}

func TestGitHubCopilotCountTokensRejectsInvalidJSON(t *testing.T) {
	executor := NewGitHubCopilotExecutor(nil)
	_, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "grok-4.5-cc",
		Payload: []byte(`{"messages":`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err == nil {
		t.Fatal("CountTokens accepted malformed JSON")
	}
}

func TestGitHubCopilotCountTokensOpenAI(t *testing.T) {
	executor := NewGitHubCopilotExecutor(nil)
	payload := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello world"}]}`)

	resp, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.prompt_tokens").Int(); got <= 0 {
		t.Fatalf("usage.prompt_tokens = %d, want positive count (body=%s)", got, string(resp.Payload))
	}
}

func TestWrapResponsesForCodexBridge(t *testing.T) {
	// Bare Copilot response object gets wrapped.
	bare := []byte(`{"object":"response","status":"completed","output":[{"type":"message"}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	wrapped := wrapResponsesForCodexBridge(bare)
	if got := gjson.GetBytes(wrapped, "type").String(); got != "response.completed" {
		t.Fatalf("type = %q, want response.completed", got)
	}
	if got := gjson.GetBytes(wrapped, "response.status").String(); got != "completed" {
		t.Fatalf("response.status = %q, want completed", got)
	}
	if got := gjson.GetBytes(wrapped, "response.output.0.type").String(); got != "message" {
		t.Fatalf("response.output.0.type = %q, want message", got)
	}

	// Already-wrapped body is returned unchanged.
	already := []byte(`{"type":"response.completed","response":{"status":"completed"}}`)
	out := wrapResponsesForCodexBridge(already)
	if got := gjson.GetBytes(out, "response.status").String(); got != "completed" {
		t.Fatalf("already-wrapped altered: %s", string(out))
	}
}
