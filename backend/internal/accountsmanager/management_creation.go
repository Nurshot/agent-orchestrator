package accountsmanager

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultCodexBaseURL  = "https://api.openai.com/v1"
	defaultClaudeBaseURL = "https://api.anthropic.com"
)

type APIKeyInput struct {
	Provider Provider
	Key      string
	BaseURL  string
}

type CredentialImport struct {
	Provider Provider
	Name     string
	JSON     json.RawMessage
}

type rawCredentialRecord struct {
	AuthIndex     string                         `json:"auth_index"`
	Name          string                         `json:"name"`
	Provider      string                         `json:"provider"`
	Type          string                         `json:"type"`
	AccountType   string                         `json:"account_type"`
	Email         string                         `json:"email"`
	Status        string                         `json:"status"`
	Disabled      bool                           `json:"disabled"`
	Unavailable   bool                           `json:"unavailable"`
	CreatedAt     time.Time                      `json:"created_at"`
	UpdatedAt     time.Time                      `json:"updated_at"`
	LastRefresh   time.Time                      `json:"last_refresh"`
	SupportsQuota bool                           `json:"supports_quota"`
	Cooldowns     []CredentialCooldown           `json:"cooldowns"`
	Quota         rawQuotaObservation            `json:"quota"`
	ModelQuota    map[string]rawQuotaObservation `json:"model_quotas"`
}

type rawQuotaObservation struct {
	ObservedAt time.Time         `json:"observed_at"`
	Signals    map[string]string `json:"signals"`
}

func (c *ManagementClient) AddAPIKey(ctx context.Context, input APIKeyInput) (CredentialSummary, error) {
	if !validProvider(input.Provider) {
		return CredentialSummary{}, ErrUnsupportedProvider
	}
	key := strings.TrimSpace(input.Key)
	if key == "" {
		return CredentialSummary{}, ErrInvalidCredential
	}
	if len(key) > managementAPIKeyLimit {
		return CredentialSummary{}, ErrRequestTooLarge
	}
	baseURL, err := normalizeProviderBaseURL(input.Provider, input.BaseURL)
	if err != nil {
		return CredentialSummary{}, err
	}

	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	endpoint, field := providerKeyEndpoint(input.Provider)
	items, err := c.readRawKeyList(ctx, endpoint, field)
	if err != nil {
		return CredentialSummary{}, err
	}
	if existing, ok := findRawAPIKey(items, input.Provider, key, baseURL); ok {
		return existing, nil
	}
	entry, err := json.Marshal(map[string]string{"api-key": key, "base-url": baseURL})
	if err != nil {
		return CredentialSummary{}, ErrInvalidCredential
	}
	items = append(items, json.RawMessage(entry))
	payload, err := json.Marshal(items)
	if err != nil {
		return CredentialSummary{}, ErrInvalidCredential
	}
	var result map[string]any
	if err = c.doJSON(ctx, "add API key", http.MethodPut, endpoint, json.RawMessage(payload), &result); err != nil {
		return CredentialSummary{}, err
	}
	items, err = c.readRawKeyList(ctx, endpoint, field)
	if err != nil {
		return CredentialSummary{}, err
	}
	if created, ok := findRawAPIKey(items, input.Provider, key, baseURL); ok {
		return created, nil
	}
	return CredentialSummary{}, ErrInvalidResponse
}

func (c *ManagementClient) ImportCredential(ctx context.Context, input CredentialImport) (CredentialSummary, error) {
	if !validProvider(input.Provider) {
		return CredentialSummary{}, ErrUnsupportedProvider
	}
	if len(input.JSON) == 0 || len(input.JSON) > managementImportLimit {
		if len(input.JSON) > managementImportLimit {
			return CredentialSummary{}, ErrRequestTooLarge
		}
		return CredentialSummary{}, ErrInvalidCredential
	}
	name, err := validateCredentialImport(input)
	if err != nil {
		return CredentialSummary{}, err
	}

	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	existing, err := c.listRawCredentials(ctx)
	if err != nil {
		return CredentialSummary{}, err
	}
	for _, record := range existing {
		if record.Name == name {
			return CredentialSummary{}, ErrCredentialConflict
		}
	}

	query := url.Values{"name": []string{name}}
	var upload map[string]any
	if err = c.doJSON(ctx, "import credential", http.MethodPost, "/v0/management/auth-files?"+query.Encode(), input.JSON, &upload); err != nil {
		return CredentialSummary{}, err
	}
	records, verifyErr := c.listRawCredentials(ctx)
	if verifyErr == nil {
		matches := matchingImportedRecords(records, name, input.Provider)
		if len(matches) == 1 {
			return summaryFromRawCredential(matches[0]), nil
		}
		if len(matches) > 1 {
			verifyErr = ErrCredentialConflict
		} else {
			verifyErr = ErrInvalidResponse
		}
	}
	var ignored map[string]any
	_ = c.doJSON(context.WithoutCancel(ctx), "remove unverified credential", http.MethodDelete, "/v0/management/auth-files?"+query.Encode(), nil, &ignored)
	return CredentialSummary{}, verifyErr
}

func (c *ManagementClient) readRawKeyList(ctx context.Context, endpoint, field string) ([]json.RawMessage, error) {
	var wrapper map[string]json.RawMessage
	if err := c.doJSON(ctx, "read API keys", http.MethodGet, endpoint, nil, &wrapper); err != nil {
		return nil, err
	}
	raw, ok := wrapper[field]
	if !ok {
		return nil, ErrInvalidResponse
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, ErrInvalidResponse
	}
	return items, nil
}

func findRawAPIKey(items []json.RawMessage, provider Provider, key, baseURL string) (CredentialSummary, bool) {
	for _, item := range items {
		var identity struct {
			Key       string `json:"api-key"`
			BaseURL   string `json:"base-url"`
			AuthIndex string `json:"auth-index"`
		}
		if json.Unmarshal(item, &identity) != nil || strings.TrimSpace(identity.Key) != key {
			continue
		}
		normalized, err := normalizeProviderBaseURL(provider, identity.BaseURL)
		if err != nil || normalized != baseURL {
			continue
		}
		ref := strings.TrimSpace(identity.AuthIndex)
		if ref == "" {
			return CredentialSummary{}, false
		}
		return CredentialSummary{Ref: ref, Provider: provider, Kind: CredentialAPIKey, Status: CredentialActive}, true
	}
	return CredentialSummary{}, false
}

func providerKeyEndpoint(provider Provider) (string, string) {
	if provider == ProviderCodex {
		return "/v0/management/codex-api-key", "codex-api-key"
	}
	return "/v0/management/claude-api-key", "claude-api-key"
}

func normalizeProviderBaseURL(provider Provider, raw string) (string, error) {
	base := strings.TrimSpace(raw)
	if base == "" {
		if provider == ProviderCodex {
			base = defaultCodexBaseURL
		} else {
			base = defaultClaudeBaseURL
		}
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidCredential
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Scheme != "https" {
		ip := net.ParseIP(parsed.Hostname())
		loopback := strings.EqualFold(parsed.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
		if parsed.Scheme != "http" || !loopback {
			return "", ErrInvalidCredential
		}
	}
	if parsed.Path != "/" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}
	return parsed.String(), nil
}

func validateCredentialImport(input CredentialImport) (string, error) {
	name := strings.TrimSpace(input.Name)
	lower := strings.ToLower(name)
	if name == "" || filepath.Base(name) != name || strings.HasPrefix(name, ".") || !strings.HasSuffix(lower, ".json") || strings.Contains(lower, "quota_probe") || strings.HasPrefix(lower, ".oauth-") {
		return "", ErrInvalidCredential
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '-' || character == '_' {
			continue
		}
		return "", ErrInvalidCredential
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(input.JSON, &object) != nil || object == nil {
		return "", ErrInvalidCredential
	}
	var documentType string
	if rawType, ok := object["type"]; !ok || json.Unmarshal(rawType, &documentType) != nil || Provider(strings.ToLower(strings.TrimSpace(documentType))) != input.Provider {
		return "", ErrInvalidCredential
	}
	if rawProbe, ok := object["quota_probe"]; ok && len(rawProbe) > 0 && string(rawProbe) != "null" {
		return "", ErrInvalidCredential
	}
	return name, nil
}

func (c *ManagementClient) listRawCredentials(ctx context.Context) ([]rawCredentialRecord, error) {
	var payload struct {
		Files []rawCredentialRecord `json:"files"`
	}
	if err := c.doJSON(ctx, "list credentials", http.MethodGet, "/v0/management/auth-files", nil, &payload); err != nil {
		return nil, err
	}
	return payload.Files, nil
}

func matchingImportedRecords(records []rawCredentialRecord, name string, provider Provider) []rawCredentialRecord {
	matches := make([]rawCredentialRecord, 0, 1)
	for _, record := range records {
		recordProvider := Provider(strings.ToLower(strings.TrimSpace(record.Provider)))
		if recordProvider == "" {
			recordProvider = Provider(strings.ToLower(strings.TrimSpace(record.Type)))
		}
		if record.Name == name && recordProvider == provider && strings.TrimSpace(record.AuthIndex) != "" {
			matches = append(matches, record)
		}
	}
	return matches
}

func summaryFromRawCredential(record rawCredentialRecord) CredentialSummary {
	provider := Provider(strings.ToLower(strings.TrimSpace(record.Provider)))
	if provider == "" {
		provider = Provider(strings.ToLower(strings.TrimSpace(record.Type)))
	}
	return CredentialSummary{
		Ref:             strings.TrimSpace(record.AuthIndex),
		Provider:        provider,
		Kind:            normalizeCredentialKind(record.AccountType),
		Email:           strings.TrimSpace(record.Email),
		Status:          normalizeCredentialState(record.Status, record.Disabled),
		Disabled:        record.Disabled,
		Unavailable:     record.Unavailable,
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
		LastRefreshedAt: record.LastRefresh,
		QuotaSupported:  record.SupportsQuota,
		Cooldowns:       append([]CredentialCooldown(nil), record.Cooldowns...),
		Quota:           projectQuotaObservation(record.Quota),
		ModelQuota:      projectModelQuotaObservations(record.ModelQuota),
	}
}

func projectQuotaObservation(raw rawQuotaObservation) CredentialQuotaObservation {
	signals := make(map[string]string, len(raw.Signals))
	for key, value := range raw.Signals {
		signals[key] = value
	}
	return CredentialQuotaObservation{ObservedAt: raw.ObservedAt, Signals: signals}
}

func projectModelQuotaObservations(raw map[string]rawQuotaObservation) map[string]CredentialQuotaObservation {
	if len(raw) == 0 {
		return nil
	}
	projected := make(map[string]CredentialQuotaObservation, len(raw))
	for model, observation := range raw {
		if trimmed := strings.TrimSpace(model); trimmed != "" {
			projected[trimmed] = projectQuotaObservation(observation)
		}
	}
	return projected
}

func normalizeCredentialKind(raw string) CredentialKind {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "oauth":
		return CredentialOAuth
	case "api_key", "api-key", "apikey":
		return CredentialAPIKey
	default:
		return CredentialUnknown
	}
}

func normalizeCredentialState(raw string, disabled bool) CredentialState {
	if disabled {
		return CredentialDisabled
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "active", "ok":
		return CredentialActive
	case "pending":
		return CredentialPending
	case "refreshing", "recovering":
		return CredentialRefreshing
	case "error", "failed", "unavailable":
		return CredentialError
	case "disabled":
		return CredentialDisabled
	default:
		return CredentialUnknownState
	}
}
