package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	copilotauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const copilotModelsCacheTTL = 10 * time.Minute

type copilotModelsCacheEntry struct {
	models    []*registry.ModelInfo
	expiresAt time.Time
}

var copilotModelsCache = struct {
	mu      sync.Mutex
	entries map[string]copilotModelsCacheEntry
}{
	entries: make(map[string]copilotModelsCacheEntry),
}

// FetchGitHubCopilotModels fetches the list of Copilot models using the supplied auth.
// Returns nil when the model list cannot be retrieved so callers can fall back.
func FetchGitHubCopilotModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	if auth == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" && auth.Attributes != nil {
		accessToken = strings.TrimSpace(auth.Attributes["access_token"])
	}
	if accessToken == "" {
		log.Debug("[copilot-models] access_token not found in auth metadata or attributes")
		return nil
	}

	if cached := loadCopilotModelsFromCache(accessToken); len(cached) > 0 {
		return cached
	}

	copilotAuth := copilotauth.NewCopilotAuth(cfg)
	apiToken, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil || apiToken == nil || apiToken.Token == "" {
		log.Warnf("[copilot-models] failed to get Copilot API token: %v", err)
		return nil
	}

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)

	vsModels := fetchModelsWithIntegrationID(ctx, httpClient, apiToken.Token, copilotIntegrationID)
	cliModels := fetchModelsWithIntegrationID(ctx, httpClient, apiToken.Token, copilotCLIIntegrationID)

	merged := mergeCopilotIntegrationModels(vsModels, cliModels)
	if len(merged) == 0 {
		return nil
	}

	log.Infof("[copilot-models] fetched %d models dynamically from Copilot API (vscode=%d, cli=%d)",
		len(merged), len(vsModels), len(cliModels))
	storeCopilotModelsInCache(accessToken, merged)
	return cloneModelInfos(merged)
}

// fetchModelsWithIntegrationID fetches Copilot models using the given integration ID.
// Tries /models then /v1/models; returns nil on failure so callers can safely merge results.
func fetchModelsWithIntegrationID(ctx context.Context, httpClient *http.Client, apiToken, integrationID string) []*registry.ModelInfo {
	paths := []string{"/models", "/v1/models"}
	for _, path := range paths {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, githubCopilotBaseURL+path, nil)
		if errReq != nil {
			return nil
		}
		applyCopilotModelHeadersWithID(req, apiToken, integrationID)
		resp, errDo := httpClient.Do(req)
		if errDo != nil {
			log.Debugf("[copilot-models][%s] request to %s failed: %v", integrationID, path, errDo)
			if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
				return nil
			}
			continue
		}

		body, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if errRead != nil {
			continue
		}
		if !isHTTPSuccess(resp.StatusCode) {
			log.Debugf("[copilot-models][%s] %s returned status %d", integrationID, path, resp.StatusCode)
			continue
		}

		models := parseCopilotModels(body)
		if len(models) > 0 {
			ids := make([]string, len(models))
			for i, m := range models {
				ids[i] = m.ID
			}
			log.Infof("[copilot-models][%s] raw model IDs from %s: %v", integrationID, path, ids)
			return models
		}
	}
	return nil
}

// mergeCopilotIntegrationModels keeps VS Code models as the default set and
// adds only CLI-exclusive models that the executor explicitly routes through
// the copilot-developer-cli integration.
func mergeCopilotIntegrationModels(vscode, cli []*registry.ModelInfo) []*registry.ModelInfo {
	seen := make(map[string]struct{}, len(vscode))
	result := make([]*registry.ModelInfo, 0, len(vscode)+len(cli))
	for _, m := range vscode {
		if m == nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(m.ID))
		if key == "" {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, m)
	}
	for _, m := range cli {
		if m == nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(m.ID))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		if copilotIntegrationIDForModel(m.ID) != copilotCLIIntegrationID {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, m)
	}
	return result
}

func applyCopilotModelHeaders(r *http.Request, apiToken string) {
	applyCopilotModelHeadersWithID(r, apiToken, copilotIntegrationID)
}

func applyCopilotModelHeadersWithID(r *http.Request, apiToken, integrationID string) {
	r.Header.Set("Authorization", "Bearer "+apiToken)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", copilotUserAgent)
	r.Header.Set("Editor-Version", copilotEditorVersion)
	r.Header.Set("Editor-Plugin-Version", copilotPluginVersion)
	r.Header.Set("Openai-Intent", copilotOpenAIIntent)
	r.Header.Set("Copilot-Integration-Id", integrationID)
	r.Header.Set("X-Github-Api-Version", copilotAPIVersion)
	r.Header.Set("X-Vscode-User-Agent-Library-Version", "electron-fetch")
}

func parseCopilotModels(body []byte) []*registry.ModelInfo {
	if len(body) == 0 {
		return nil
	}
	now := time.Now().Unix()
	seen := make(map[string]struct{})
	models := make([]*registry.ModelInfo, 0)

	addModel := func(modelID, ownedBy string, created int64) {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			return
		}
		if _, ok := seen[modelID]; ok {
			return
		}
		seen[modelID] = struct{}{}
		if created == 0 {
			created = now
		}
		if strings.TrimSpace(ownedBy) == "" {
			ownedBy = "github-copilot"
		}
		info := &registry.ModelInfo{
			ID:      modelID,
			Object:  "model",
			Created: created,
			OwnedBy: ownedBy,
			Type:    "github-copilot",
		}

		// Merge metadata from static model definitions (SupportedEndpoints, ContextLength, etc.)
		if staticInfo := findStaticCopilotModel(modelID); staticInfo != nil {
			info.SupportedEndpoints = staticInfo.SupportedEndpoints
			info.ContextLength = staticInfo.ContextLength
			info.MaxCompletionTokens = staticInfo.MaxCompletionTokens
			info.DisplayName = staticInfo.DisplayName
			info.Description = staticInfo.Description
			info.Thinking = staticInfo.Thinking
		} else {
			// For models not in the static list, infer defaults from the model name
			info.SupportedEndpoints = inferSupportedEndpoints(modelID)
			info.ContextLength = 200000
			info.MaxCompletionTokens = 64000
		}

		models = append(models, info)
	}

	// addModelWithCaps is like addModel but accepts optional capabilities from the API response
	addModelWithCaps := func(modelID, ownedBy string, created int64, rawItem gjson.Result) {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			return
		}
		if _, ok := seen[modelID]; ok {
			return
		}
		seen[modelID] = struct{}{}
		if created == 0 {
			created = now
		}
		if strings.TrimSpace(ownedBy) == "" {
			ownedBy = "github-copilot"
		}
		info := &registry.ModelInfo{
			ID:      modelID,
			Object:  "model",
			Created: created,
			OwnedBy: ownedBy,
			Type:    "github-copilot",
		}

		// Merge metadata from static model definitions (SupportedEndpoints, ContextLength, etc.)
		if staticInfo := findStaticCopilotModel(modelID); staticInfo != nil {
			info.SupportedEndpoints = staticInfo.SupportedEndpoints
			info.ContextLength = staticInfo.ContextLength
			info.MaxCompletionTokens = staticInfo.MaxCompletionTokens
			info.DisplayName = staticInfo.DisplayName
			info.Description = staticInfo.Description
			info.Thinking = staticInfo.Thinking
		} else {
			info.SupportedEndpoints = inferSupportedEndpoints(modelID)
			info.MaxCompletionTokens = 64000

			// Try to extract context window from API response capabilities
			contextLen := int(0)
			// Check capabilities.limits.max_prompt_tokens (GitHub Copilot format)
			if v := rawItem.Get("capabilities.limits.max_prompt_tokens"); v.Exists() && v.Int() > 0 {
				contextLen = int(v.Int())
			}
			// Check capabilities.tokenLimits.maxInputTokens
			if contextLen == 0 {
				if v := rawItem.Get("capabilities.tokenLimits.maxInputTokens"); v.Exists() && v.Int() > 0 {
					contextLen = int(v.Int())
				}
			}
			// Check top-level context_length / context_window
			if contextLen == 0 {
				if v := rawItem.Get("context_length"); v.Exists() && v.Int() > 0 {
					contextLen = int(v.Int())
				}
			}
			if contextLen == 0 {
				if v := rawItem.Get("context_window"); v.Exists() && v.Int() > 0 {
					contextLen = int(v.Int())
				}
			}

			if contextLen > 0 {
				info.ContextLength = contextLen
				log.Debugf("[copilot-models] model %s: context length from API = %d", modelID, contextLen)
			} else {
				info.ContextLength = 200000
			}

			// Try to extract max output tokens from API
			if v := rawItem.Get("capabilities.limits.max_output_tokens"); v.Exists() && v.Int() > 0 {
				info.MaxCompletionTokens = int(v.Int())
			}
		}

		models = append(models, info)
	}

	data := gjson.GetBytes(body, "data")
	switch {
	case data.Exists() && data.IsArray():
		for _, item := range data.Array() {
			if item.IsObject() {
				ownedBy := item.Get("owned_by").String()
				if ownedBy == "" {
					ownedBy = item.Get("vendor").String()
				}
				addModelWithCaps(item.Get("id").String(), ownedBy, modelCreatedAt(item), item)
				continue
			}
			if item.Type == gjson.String {
				addModel(item.String(), "", 0)
			}
		}
	case data.Exists() && data.Type == gjson.String:
		addModel(data.String(), "", 0)
	}

	if len(models) > 0 {
		return models
	}

	modelsNode := gjson.GetBytes(body, "models")
	if modelsNode.Exists() {
		if modelsNode.IsArray() {
			for _, item := range modelsNode.Array() {
				if item.IsObject() {
					addModelWithCaps(item.Get("id").String(), item.Get("owned_by").String(), modelCreatedAt(item), item)
					continue
				}
				if item.Type == gjson.String {
					addModel(item.String(), "", 0)
				}
			}
		} else if modelsNode.IsObject() {
			for key := range modelsNode.Map() {
				addModel(key, "", 0)
			}
		}
	}

	return models
}

func modelCreatedAt(item gjson.Result) int64 {
	if item.Get("created").Exists() {
		return item.Get("created").Int()
	}
	if item.Get("created_at").Exists() {
		return item.Get("created_at").Int()
	}
	return 0
}

func loadCopilotModelsFromCache(accessToken string) []*registry.ModelInfo {
	copilotModelsCache.mu.Lock()
	defer copilotModelsCache.mu.Unlock()

	entry, ok := copilotModelsCache.entries[accessToken]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil
	}
	return cloneModelInfos(entry.models)
}

func storeCopilotModelsInCache(accessToken string, models []*registry.ModelInfo) {
	if accessToken == "" || len(models) == 0 {
		return
	}
	copilotModelsCache.mu.Lock()
	copilotModelsCache.entries[accessToken] = copilotModelsCacheEntry{
		models:    cloneModelInfos(models),
		expiresAt: time.Now().Add(copilotModelsCacheTTL),
	}
	copilotModelsCache.mu.Unlock()
}

func cloneModelInfos(models []*registry.ModelInfo) []*registry.ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]*registry.ModelInfo, len(models))
	copy(out, models)
	return out
}

// findStaticCopilotModel looks up a model by ID in the static GitHub Copilot model list.
// Returns nil if no match is found.
func findStaticCopilotModel(modelID string) *registry.ModelInfo {
	for _, m := range registry.GetGitHubCopilotModels() {
		if m.ID == modelID {
			return m
		}
	}
	return nil
}

// inferSupportedEndpoints determines the supported endpoints for a dynamically
// discovered model based on its brand/naming convention:
//   - GPT / OpenAI series (gpt-*, o1-*, o3-*, o4-*) → /responses
//   - MAI code models → /responses
//   - Claude series → /chat/completions (GitHub Copilot proxies Claude via chat completions)
//   - Everything else → /chat/completions
func inferSupportedEndpoints(modelID string) []string {
	lower := strings.ToLower(modelID)
	if strings.HasPrefix(lower, "gpt-") ||
		strings.HasPrefix(lower, "mai-code-") ||
		strings.HasPrefix(lower, "o1") ||
		strings.HasPrefix(lower, "o3") ||
		strings.HasPrefix(lower, "o4") {
		return []string{"/responses"}
	}
	return []string{"/chat/completions"}
}
