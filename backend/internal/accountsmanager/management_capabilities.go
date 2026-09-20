package accountsmanager

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type CredentialModel struct {
	ID          string
	DisplayName string
	Type        string
	Owner       string
}

type QuotaSubscription struct {
	Plan     string `json:"plan"`
	TierName string `json:"tierName"`
	TierID   string `json:"tierId"`
}

type QuotaMetric struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit"`
	Format   string  `json:"format"`
	Currency string  `json:"currency"`
}

type QuotaBucket struct {
	Window            string  `json:"window"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime"`
	Description       string  `json:"description"`
}

type QuotaGroup struct {
	DisplayName string        `json:"displayName"`
	Buckets     []QuotaBucket `json:"buckets"`
}

type CredentialQuota struct {
	Subscription       *QuotaSubscription `json:"subscription"`
	Summary            []QuotaMetric      `json:"summary"`
	ServerTimeOffsetMS int64              `json:"serverTimeOffsetMs"`
	Groups             []QuotaGroup       `json:"groups"`
}

func (c *ManagementClient) ListCredentialModels(ctx context.Context, ref string) ([]CredentialModel, error) {
	record, err := c.resolveCredential(ctx, ref)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(record.Name)
	if name == "" {
		return nil, ErrOperationUnsupported
	}
	query := url.Values{"name": []string{name}}
	var response struct {
		Models []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Type        string `json:"type"`
			Owner       string `json:"owned_by"`
		} `json:"models"`
	}
	if err = c.doJSON(ctx, "list credential models", http.MethodGet, "/v0/management/auth-files/models?"+query.Encode(), nil, &response); err != nil {
		return nil, err
	}
	if len(response.Models) > 4096 {
		return nil, ErrInvalidResponse
	}
	models := make([]CredentialModel, 0, len(response.Models))
	seen := make(map[string]struct{}, len(response.Models))
	for _, raw := range response.Models {
		id := strings.TrimSpace(raw.ID)
		if id == "" || len(id) > 512 {
			return nil, ErrInvalidResponse
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, CredentialModel{
			ID:          id,
			DisplayName: strings.TrimSpace(raw.DisplayName),
			Type:        strings.TrimSpace(raw.Type),
			Owner:       strings.TrimSpace(raw.Owner),
		})
	}
	return models, nil
}

func (c *ManagementClient) FetchCredentialQuota(ctx context.Context, ref string) (CredentialQuota, error) {
	record, err := c.resolveCredential(ctx, ref)
	if err != nil {
		return CredentialQuota{}, err
	}
	if !record.SupportsQuota {
		return CredentialQuota{}, ErrOperationUnsupported
	}
	var quota CredentialQuota
	err = c.doJSON(ctx, "fetch credential quota", http.MethodPost, "/v0/management/quota/fetch", map[string]string{"auth_index": record.AuthIndex}, &quota)
	if err != nil {
		var statusErr *ManagementStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotImplemented {
			return CredentialQuota{}, ErrOperationUnsupported
		}
		return CredentialQuota{}, err
	}
	if !validCredentialQuota(quota) {
		return CredentialQuota{}, ErrInvalidResponse
	}
	return quota, nil
}

func (c *ManagementClient) ResetCredentialQuota(ctx context.Context, ref string) error {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	record, err := c.resolveCredential(ctx, ref)
	if err != nil {
		return err
	}
	if !record.SupportsQuota {
		return ErrOperationUnsupported
	}
	provider := Provider(strings.ToLower(strings.TrimSpace(record.Provider)))
	if provider == "" {
		provider = Provider(strings.ToLower(strings.TrimSpace(record.Type)))
	}
	var providers struct {
		Providers []struct {
			Provider           string   `json:"provider"`
			SupportedProviders []string `json:"supported_providers"`
			SupportsReset      bool     `json:"supports_reset"`
		} `json:"providers"`
	}
	if err = c.doJSON(ctx, "list quota providers", http.MethodGet, "/v0/management/quota/providers", nil, &providers); err != nil {
		return err
	}
	resetSupported := false
	for _, candidate := range providers.Providers {
		if !candidate.SupportsReset {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(candidate.Provider), string(provider)) {
			resetSupported = true
			break
		}
		for _, supported := range candidate.SupportedProviders {
			if strings.EqualFold(strings.TrimSpace(supported), string(provider)) {
				resetSupported = true
				break
			}
		}
		if resetSupported {
			break
		}
	}
	if !resetSupported {
		return ErrOperationUnsupported
	}
	var response map[string]any
	err = c.doJSON(ctx, "reset credential quota", http.MethodPost, "/v0/management/quota/reset", map[string]string{"auth_index": record.AuthIndex}, &response)
	var statusErr *ManagementStatusError
	if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotImplemented {
		return ErrOperationUnsupported
	}
	return err
}

func validCredentialQuota(quota CredentialQuota) bool {
	if len(quota.Summary) > 256 || len(quota.Groups) > 128 {
		return false
	}
	for _, metric := range quota.Summary {
		if strings.TrimSpace(metric.Key) == "" || len(metric.Key) > 256 || len(metric.Label) > 512 {
			return false
		}
	}
	for _, group := range quota.Groups {
		if len(group.Buckets) > 256 {
			return false
		}
		for _, bucket := range group.Buckets {
			if bucket.RemainingFraction < 0 || bucket.RemainingFraction > 1 {
				return false
			}
		}
	}
	return true
}
