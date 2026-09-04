package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

type responsesChatFallbackExecutor struct {
	sourceFormats []string
	models        []string
	payloads      [][]byte
}

func (e *responsesChatFallbackExecutor) Identifier() string { return "test-responses-chat-fallback" }

func (e *responsesChatFallbackExecutor) capture(req coreexecutor.Request, opts coreexecutor.Options) {
	e.sourceFormats = append(e.sourceFormats, opts.SourceFormat.String())
	e.models = append(e.models, req.Model)
	e.payloads = append(e.payloads, append([]byte(nil), req.Payload...))
}

func (e *responsesChatFallbackExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.capture(req, opts)
	return coreexecutor.Response{Payload: []byte(`{"id":"chatcmpl-gemini38-responses","object":"chat.completion","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"RESPONSES_OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":5,"total_tokens":14}}`)}, nil
}

func (e *responsesChatFallbackExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (<-chan coreexecutor.StreamChunk, error) {
	e.capture(req, opts)
	stream := make(chan coreexecutor.StreamChunk, 3)
	stream <- coreexecutor.StreamChunk{Payload: []byte(`data: {"id":"chatcmpl-gemini38-responses","object":"chat.completion.chunk","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"STREAM_OK"},"finish_reason":null}]}`)}
	stream <- coreexecutor.StreamChunk{Payload: []byte(`data: {"id":"chatcmpl-gemini38-responses","object":"chat.completion.chunk","created":1,"model":"gemini-3.8-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":5,"total_tokens":14}}`)}
	stream <- coreexecutor.StreamChunk{Payload: []byte(`data: [DONE]`)}
	close(stream)
	return stream, nil
}

func (e *responsesChatFallbackExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesChatFallbackExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesChatFallbackExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIResponsesUsesChatFallbackForGemini38Alias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &responsesChatFallbackExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "gemini38-responses-fallback", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{
		ID:                 "gemini-3.8-flash-cc",
		SupportedEndpoints: []string{"/chat/completions"},
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", handler.Responses)

	tests := []struct {
		name       string
		request    string
		wantMarker string
		wantStream bool
	}{
		{
			name:       "non-streaming",
			request:    `{"model":"gemini-3.8-flash-cc","input":"reply with the marker","stream":false}`,
			wantMarker: "RESPONSES_OK",
		},
		{
			name:       "streaming",
			request:    `{"model":"gemini-3.8-flash-cc","input":"reply with the marker","stream":true}`,
			wantMarker: "STREAM_OK",
			wantStream: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tc.request))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)

			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
			}
			if !strings.Contains(resp.Body.String(), tc.wantMarker) {
				t.Fatalf("response missing %q: %s", tc.wantMarker, resp.Body.String())
			}
			if tc.wantStream && !strings.Contains(resp.Body.String(), "response.output_text.delta") {
				t.Fatalf("stream missing Responses output_text event: %s", resp.Body.String())
			}
		})
	}

	if len(executor.payloads) != 2 {
		t.Fatalf("executor calls = %d, want 2", len(executor.payloads))
	}
	for i, payload := range executor.payloads {
		if executor.sourceFormats[i] != "openai" {
			t.Fatalf("request %d source format = %q, want openai", i, executor.sourceFormats[i])
		}
		if executor.models[i] != "gemini-3.8-flash-cc" {
			t.Fatalf("request %d model = %q, want gemini-3.8-flash-cc", i, executor.models[i])
		}
		if gjson.GetBytes(payload, "input").Exists() || !gjson.GetBytes(payload, "messages").IsArray() {
			t.Fatalf("request %d was not converted to Chat Completions: %s", i, string(payload))
		}
	}
}
