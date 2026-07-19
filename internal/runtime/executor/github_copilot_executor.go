package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	copilotauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	githubCopilotBaseURL       = "https://api.githubcopilot.com"
	githubCopilotChatPath      = "/chat/completions"
	githubCopilotResponsesPath = "/responses"
	githubCopilotAuthType      = "github-copilot"
	githubCopilotTokenCacheTTL = 25 * time.Minute
	// tokenExpiryBuffer is the time before expiry when we should refresh the token.
	tokenExpiryBuffer = 5 * time.Minute
	// maxScannerBufferSize is the maximum buffer size for SSE scanning (20MB).
	maxScannerBufferSize = 20_971_520

	// Copilot API header values — keep in sync with latest copilot-api / VS Code.
	copilotChatVersion      = "0.48.1"
	copilotUserAgent        = "GitHubCopilotChat/" + copilotChatVersion
	copilotEditorVersion    = "vscode/1.126.0"
	copilotPluginVersion    = "copilot-chat/" + copilotChatVersion
	copilotIntegrationID    = "vscode-chat"
	copilotCLIIntegrationID = "copilot-developer-cli"
	copilotOpenAIIntent     = "conversation-panel"
	copilotAPIVersion       = "2025-04-01"
)

// GitHubCopilotExecutor handles requests to the GitHub Copilot API.
type GitHubCopilotExecutor struct {
	cfg   *config.Config
	mu    sync.RWMutex
	cache map[string]*cachedAPIToken
}

// cachedAPIToken stores a cached Copilot API token with its expiry.
type cachedAPIToken struct {
	token     string
	expiresAt time.Time
}

// NewGitHubCopilotExecutor constructs a new executor instance.
func NewGitHubCopilotExecutor(cfg *config.Config) *GitHubCopilotExecutor {
	return &GitHubCopilotExecutor{
		cfg:   cfg,
		cache: make(map[string]*cachedAPIToken),
	}
}

// Identifier implements ProviderExecutor.
func (e *GitHubCopilotExecutor) Identifier() string { return githubCopilotAuthType }

// PrepareRequest implements ProviderExecutor.
func (e *GitHubCopilotExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	ctx := req.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return errToken
	}
	e.applyHeaders(req, apiToken)
	return nil
}

// HttpRequest injects GitHub Copilot credentials into the request and executes it.
func (e *GitHubCopilotExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("github-copilot executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute handles non-streaming requests to GitHub Copilot.
func (e *GitHubCopilotExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	bridgeModel, bridgeActive := claudeResponsesBridgeModel(req.Model, from)
	useResponses := useGitHubCopilotResponsesEndpoint(from) || bridgeActive
	to := sdktranslator.FromString("openai")
	if bridgeActive {
		to = sdktranslator.FromString("codex")
	} else if useResponses {
		to = sdktranslator.FromString("openai-response")
	}
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, req.Model, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	body = e.normalizeModel(req.Model, body)
	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", body, originalTranslated, requestedModel)
	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), "github-copilot")
	if err != nil {
		log.Debugf("github-copilot executor: thinking validation: %v", err)
	}
	body = normalizeCopilotEffort(body)
	if bridgeActive {
		body = rewriteBridgeModel(body, bridgeModel)
	}
	body, _ = sjson.SetBytes(body, "stream", false)

	path := githubCopilotChatPath
	if useResponses {
		path = githubCopilotResponsesPath
	}
	url := githubCopilotBaseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	e.applyHeaders(httpReq, apiToken)
	httpReq.Header.Set("Copilot-Integration-Id", copilotIntegrationIDForModel(req.Model))

	// Add Copilot-Vision-Request header if the request contains vision content
	if detectVisionContent(body) {
		httpReq.Header.Set("Copilot-Vision-Request", "true")
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("github-copilot executor: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if !isHTTPSuccess(httpResp.StatusCode) {
		data, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, data)
		log.Debugf("github-copilot executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return resp, err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	detail := parseOpenAIUsage(data)
	if useResponses && detail.TotalTokens == 0 {
		detail = parseOpenAIResponsesUsage(data)
	}
	if detail.TotalTokens > 0 {
		reporter.publish(ctx, detail)
	}

	if bridgeActive {
		data = wrapResponsesForCodexBridge(data)
	}

	var param any
	converted := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, data, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(converted)}
	reporter.ensurePublished(ctx)
	return resp, nil
}

// ExecuteStream handles streaming requests to GitHub Copilot.
func (e *GitHubCopilotExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	bridgeModel, bridgeActive := claudeResponsesBridgeModel(req.Model, from)
	useResponses := useGitHubCopilotResponsesEndpoint(from) || bridgeActive
	to := sdktranslator.FromString("openai")
	if bridgeActive {
		to = sdktranslator.FromString("codex")
	} else if useResponses {
		to = sdktranslator.FromString("openai-response")
	}
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, req.Model, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	body = e.normalizeModel(req.Model, body)
	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", body, originalTranslated, requestedModel)
	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), "github-copilot")
	if err != nil {
		log.Debugf("github-copilot executor: thinking validation: %v", err)
	}
	body = normalizeCopilotEffort(body)
	if bridgeActive {
		body = rewriteBridgeModel(body, bridgeModel)
	}
	body, _ = sjson.SetBytes(body, "stream", true)
	// Enable stream options for usage stats in stream
	if !useResponses {
		body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)
	}

	path := githubCopilotChatPath
	if useResponses {
		path = githubCopilotResponsesPath
	}
	url := githubCopilotBaseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	e.applyHeaders(httpReq, apiToken)
	httpReq.Header.Set("Copilot-Integration-Id", copilotIntegrationIDForModel(req.Model))

	// Add Copilot-Vision-Request header if the request contains vision content
	if detectVisionContent(body) {
		httpReq.Header.Set("Copilot-Vision-Request", "true")
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if !isHTTPSuccess(httpResp.StatusCode) {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("github-copilot executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			recordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		log.Debugf("github-copilot executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out

	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("github-copilot executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, maxScannerBufferSize)
		var param any

		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)

			// Parse SSE data
			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				if bytes.Equal(data, []byte("[DONE]")) {
					continue
				}
				if detail, ok := parseOpenAIStreamUsage(line); ok {
					reporter.publish(ctx, detail)
				} else if useResponses {
					if detail, ok := parseOpenAIResponsesStreamUsage(line); ok {
						reporter.publish(ctx, detail)
					}
				}
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		} else {
			reporter.ensurePublished(ctx)
		}
	}()

	return stream, nil
}

// CountTokens is not supported for GitHub Copilot.
func (e *GitHubCopilotExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "count tokens not supported for github-copilot"}
}

// Refresh validates the GitHub token is still working.
// GitHub OAuth tokens don't expire traditionally, so we just validate.
func (e *GitHubCopilotExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}

	// Get the GitHub access token
	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return auth, nil
	}

	// Validate the token can still get a Copilot API token
	copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
	_, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: fmt.Sprintf("github-copilot token validation failed: %v", err)}
	}

	return auth, nil
}

// ensureAPIToken gets or refreshes the Copilot API token.
func (e *GitHubCopilotExecutor) ensureAPIToken(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}

	// Get the GitHub access token
	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return "", statusErr{code: http.StatusUnauthorized, msg: "missing github access token"}
	}

	// Check for cached API token using thread-safe access
	e.mu.RLock()
	if cached, ok := e.cache[accessToken]; ok && cached.expiresAt.After(time.Now().Add(tokenExpiryBuffer)) {
		e.mu.RUnlock()
		return cached.token, nil
	}
	e.mu.RUnlock()

	// Get a new Copilot API token
	copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
	apiToken, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil {
		return "", statusErr{code: http.StatusUnauthorized, msg: fmt.Sprintf("failed to get copilot api token: %v", err)}
	}

	// Cache the token with thread-safe access
	expiresAt := time.Now().Add(githubCopilotTokenCacheTTL)
	if apiToken.ExpiresAt > 0 {
		expiresAt = time.Unix(apiToken.ExpiresAt, 0)
	}
	e.mu.Lock()
	e.cache[accessToken] = &cachedAPIToken{
		token:     apiToken.Token,
		expiresAt: expiresAt,
	}
	e.mu.Unlock()

	return apiToken.Token, nil
}

// applyHeaders sets the required headers for GitHub Copilot API requests.
func (e *GitHubCopilotExecutor) applyHeaders(r *http.Request, apiToken string) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiToken)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", copilotUserAgent)
	r.Header.Set("Editor-Version", copilotEditorVersion)
	r.Header.Set("Editor-Plugin-Version", copilotPluginVersion)
	r.Header.Set("Openai-Intent", copilotOpenAIIntent)
	r.Header.Set("Copilot-Integration-Id", copilotIntegrationID)
	r.Header.Set("X-Github-Api-Version", copilotAPIVersion)
	r.Header.Set("X-Request-Id", uuid.NewString())
	r.Header.Set("X-Vscode-User-Agent-Library-Version", "electron-fetch")
}

// detectVisionContent checks if the request body contains vision/image content.
// Returns true if the request includes image_url or image type content blocks.
func detectVisionContent(body []byte) bool {
	// Parse messages array
	messagesResult := gjson.GetBytes(body, "messages")
	if !messagesResult.Exists() || !messagesResult.IsArray() {
		return false
	}

	// Check each message for vision content
	for _, message := range messagesResult.Array() {
		content := message.Get("content")

		// If content is an array, check each content block
		if content.IsArray() {
			for _, block := range content.Array() {
				blockType := block.Get("type").String()
				// Check for image_url or image type
				if blockType == "image_url" || blockType == "image" {
					return true
				}
			}
		}
	}

	return false
}

// normalizeModel is a no-op as GitHub Copilot accepts model names directly.
// Model mapping should be done at the registry level if needed.
func (e *GitHubCopilotExecutor) normalizeModel(_ string, body []byte) []byte {
	return body
}

// isCopilotCLIModel reports whether the model requires the copilot-developer-cli
// integration ID. CLI-exclusive models (e.g. 1M-context variants) are identified
// by the "-1m" suffix in their base name.
func isCopilotCLIModel(model string) bool {
	base := strings.ToLower(thinking.ParseSuffix(model).ModelName)
	return strings.HasSuffix(base, "-1m")
}

func copilotIntegrationIDForModel(model string) string {
	if isCopilotCLIModel(model) {
		return copilotCLIIntegrationID
	}
	return copilotIntegrationID
}

func useGitHubCopilotResponsesEndpoint(sourceFormat sdktranslator.Format) bool {
	return sourceFormat.String() == "openai-response"
}

// claudeBridgeSuffix marks Claude-client aliases for Responses-only Copilot models.
// A request for "gpt-5.6-sol-cc" from a Claude client is bridged to the real
// upstream model "gpt-5.6-sol" over the /responses endpoint.
const claudeBridgeSuffix = "-cc"

// claudeResponsesBridgeModel reports whether the request is a Claude-client call
// for one of the "-cc" bridge aliases. When it is, the returned string is the real
// upstream model name (suffix stripped) and the boolean is true. The bridge only
// activates for the Claude source format; every other client (including native
// Responses clients hitting the un-suffixed model) is left untouched.
func claudeResponsesBridgeModel(model string, sourceFormat sdktranslator.Format) (string, bool) {
	if sourceFormat.String() != "claude" {
		return model, false
	}
	base := thinking.ParseSuffix(model).ModelName
	if !strings.HasSuffix(strings.ToLower(base), claudeBridgeSuffix) {
		return model, false
	}
	upstream := base[:len(base)-len(claudeBridgeSuffix)]
	if upstream == "" {
		return model, false
	}
	return upstream, true
}

// rewriteBridgeModel replaces the translated body's "model" field with the real
// upstream model name. The Claude->Codex translator writes the client-facing alias
// (e.g. "gpt-5.6-sol-cc") into the payload; Copilot only recognises the real name.
func rewriteBridgeModel(body []byte, upstreamModel string) []byte {
	if upstreamModel == "" {
		return body
	}
	if updated, err := sjson.SetBytes(body, "model", upstreamModel); err == nil {
		return updated
	}
	return body
}

// wrapResponsesForCodexBridge adapts a non-streaming Copilot /responses body into
// the envelope the Codex->Claude translator expects. Copilot returns a bare
// response object ({"object":"response","status":"completed",...}), whereas
// ConvertCodexResponseToClaudeNonStream reads {"type":"response.completed",
// "response":{...}}. If the body is already wrapped it is returned unchanged.
func wrapResponsesForCodexBridge(data []byte) []byte {
	if gjson.GetBytes(data, "type").String() == "response.completed" {
		return data
	}
	if !gjson.ValidBytes(data) {
		return data
	}
	wrapped, err := sjson.SetRawBytes([]byte(`{"type":"response.completed"}`), "response", data)
	if err != nil {
		return data
	}
	return wrapped
}

// normalizeCopilotEffort rewrites reasoning-effort values that GitHub Copilot's
// upstream rejects into the closest value it accepts. The Codex client exposes an
// "ultra" tier above Copilot's ceiling of "max"; without this remap the upstream
// returns a 400 ("Invalid value: 'ultra'"). It handles both the Responses schema
// (reasoning.effort) and the Chat Completions schema (reasoning_effort), and is a
// no-op for any value that is not "ultra".
func normalizeCopilotEffort(body []byte) []byte {
	for _, path := range []string{"reasoning.effort", "reasoning_effort"} {
		v := gjson.GetBytes(body, path)
		if v.Exists() && strings.EqualFold(strings.TrimSpace(v.String()), "ultra") {
			if updated, err := sjson.SetBytes(body, path, "max"); err == nil {
				body = updated
			}
		}
	}
	return body
}

// isHTTPSuccess checks if the status code indicates success (2xx).
func isHTTPSuccess(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}
