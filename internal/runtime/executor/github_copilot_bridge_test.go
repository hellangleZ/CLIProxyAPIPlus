package executor

import (
	"context"
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
		{"high unchanged", "gpt-5.5-cc", `{"reasoning":{"effort":"high"}}`, "reasoning.effort", "high"},
		{"ultra remains global max", "gpt-5.6-sol-cc", `{"reasoning":{"effort":"ultra"}}`, "reasoning.effort", "max"},
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

func TestNewGitHubCopilotStatusErrNormalizesGrokPromptLimit(t *testing.T) {
	body := []byte(`{"error":{"code":"invalid_request_body","message":"{\"code\":\"invalid-argument\",\"error\":\"This model's maximum prompt length is 500000 but the request contains 500271 tokens.\"}"}}`)
	want := "prompt is too long: 500271 tokens > 500000"

	for _, model := range []string{
		"grok-4.5-cc",
		"grok-4.6-cc",
		"grok-4.6-cc[1m]",
		"grok-4.6-cc[1m](high)",
	} {
		t.Run(model, func(t *testing.T) {
			err := newGitHubCopilotStatusErr(http.StatusBadRequest, body, model)
			if err.code != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d", err.code, http.StatusBadRequest)
			}
			if err.msg != want {
				t.Fatalf("message = %q, want %q", err.msg, want)
			}
		})
	}
}

func TestNewGitHubCopilotStatusErrLeavesUnrelatedErrorsUntouched(t *testing.T) {
	contextLimitBody := []byte(`{"error":{"code":"invalid_request_body","message":"{\"code\":\"invalid-argument\",\"error\":\"This model's maximum prompt length is 500000 but the request contains 500271 tokens.\"}"}}`)

	tests := []struct {
		name   string
		status int
		model  string
		body   []byte
	}{
		{
			name:   "gpt bridge alias",
			status: http.StatusBadRequest,
			model:  "gpt-5.6-sol-cc",
			body:   contextLimitBody,
		},
		{
			name:   "plain grok model",
			status: http.StatusBadRequest,
			model:  "grok-4.6",
			body:   contextLimitBody,
		},
		{
			name:   "non bad request status",
			status: http.StatusTooManyRequests,
			model:  "grok-4.6-cc",
			body:   contextLimitBody,
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

func TestGitHubCopilotExecutePathsNormalizeGrokPromptLimit(t *testing.T) {
	const upstreamBody = `{"error":{"code":"invalid_request_body","message":"{\"code\":\"invalid-argument\",\"error\":\"This model's maximum prompt length is 500000 but the request contains 500271 tokens.\"}"}}`
	transport := githubCopilotRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
			Request:    req,
		}, nil
	})

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
	payload := []byte(`{"model":"grok-4.6-cc","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{Model: "grok-4.6-cc", Payload: payload}
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
					t.Fatal("streaming bootstrap returned a stream for an upstream 400")
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
			if got := err.Error(); got != "prompt is too long: 500271 tokens > 500000" {
				t.Fatalf("error = %q, want normalized prompt limit", got)
			}
			statusErr, ok := err.(interface{ StatusCode() int })
			if !ok || statusErr.StatusCode() != http.StatusBadRequest {
				t.Fatalf("status = %v, want %d", statusErr, http.StatusBadRequest)
			}
		})
	}
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
