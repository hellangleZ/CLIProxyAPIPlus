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
		return nil
	}

	if cached := loadCopilotModelsFromCache(accessToken); len(cached) > 0 {
		return cached
	}

	copilotAuth := copilotauth.NewCopilotAuth(cfg)
	apiToken, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil || apiToken == nil || apiToken.Token == "" {
		return nil
	}

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	paths := []string{"/models", "/v1/models"}
	for _, path := range paths {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, githubCopilotBaseURL+path, nil)
		if errReq != nil {
			return nil
		}
		applyCopilotModelHeaders(req, apiToken.Token)
		resp, errDo := httpClient.Do(req)
		if errDo != nil {
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
			continue
		}

		models := parseCopilotModels(body)
		if len(models) > 0 {
			storeCopilotModelsInCache(accessToken, models)
			return cloneModelInfos(models)
		}
	}

	return nil
}

func applyCopilotModelHeaders(r *http.Request, apiToken string) {
	r.Header.Set("Authorization", "Bearer "+apiToken)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", copilotUserAgent)
	r.Header.Set("Editor-Version", copilotEditorVersion)
	r.Header.Set("Editor-Plugin-Version", copilotPluginVersion)
	r.Header.Set("Openai-Intent", copilotOpenAIIntent)
	r.Header.Set("Copilot-Integration-Id", copilotIntegrationID)
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
		models = append(models, &registry.ModelInfo{
			ID:      modelID,
			Object:  "model",
			Created: created,
			OwnedBy: ownedBy,
			Type:    "github-copilot",
		})
	}

	data := gjson.GetBytes(body, "data")
	switch {
	case data.Exists() && data.IsArray():
		for _, item := range data.Array() {
			if item.IsObject() {
				addModel(item.Get("id").String(), item.Get("owned_by").String(), modelCreatedAt(item))
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
					addModel(item.Get("id").String(), item.Get("owned_by").String(), modelCreatedAt(item))
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
