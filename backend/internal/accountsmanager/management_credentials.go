package accountsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

func (c *ManagementClient) SetCredentialDisabled(ctx context.Context, ref string, disabled bool) error {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	record, err := c.resolveCredential(ctx, ref)
	if err != nil {
		return err
	}
	if strings.TrimSpace(record.Name) == "" {
		return ErrOperationUnsupported
	}
	request := struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		Disabled  bool   `json:"disabled"`
	}{Name: record.Name, AuthIndex: record.AuthIndex, Disabled: disabled}
	var response map[string]any
	return mapCredentialOperationError(c.doJSON(ctx, "set credential status", http.MethodPatch, "/v0/management/auth-files/status", request, &response))
}

func (c *ManagementClient) RefreshCredential(ctx context.Context, ref string) (CredentialSummary, error) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	record, err := c.resolveCredential(ctx, ref)
	if err != nil {
		return CredentialSummary{}, err
	}
	if strings.TrimSpace(record.Name) == "" {
		return CredentialSummary{}, ErrOperationUnsupported
	}
	var response map[string]any
	if err = c.doJSON(ctx, "refresh credential", http.MethodPost, "/v0/management/auth-files/refresh", map[string]string{"name": record.Name}, &response); err != nil {
		return CredentialSummary{}, mapCredentialOperationError(err)
	}
	updated, err := c.resolveCredential(ctx, ref)
	if err != nil {
		return CredentialSummary{}, err
	}
	return summaryFromRawCredential(updated), nil
}

func (c *ManagementClient) RemoveCredential(ctx context.Context, ref string) error {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	record, err := c.resolveCredential(ctx, ref)
	if errors.Is(err, ErrCredentialNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if normalizeCredentialKind(record.AccountType) == CredentialAPIKey {
		return c.removeAPIKeyCredential(ctx, record)
	}
	name := strings.TrimSpace(record.Name)
	if name == "" || len(name) > 255 || filepath.Base(name) != name || strings.HasPrefix(name, ".") || !strings.HasSuffix(strings.ToLower(name), ".json") {
		return ErrOperationUnsupported
	}
	query := url.Values{"name": []string{name}}
	var response map[string]any
	err = c.doJSON(ctx, "remove credential", http.MethodDelete, "/v0/management/auth-files?"+query.Encode(), nil, &response)
	var statusErr *ManagementStatusError
	if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *ManagementClient) resolveCredential(ctx context.Context, ref string) (rawCredentialRecord, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return rawCredentialRecord{}, ErrCredentialNotFound
	}
	records, err := c.listRawCredentials(ctx)
	if err != nil {
		return rawCredentialRecord{}, err
	}
	matches := make([]rawCredentialRecord, 0, 1)
	for _, record := range records {
		provider := Provider(strings.ToLower(strings.TrimSpace(record.Provider)))
		if provider == "" {
			provider = Provider(strings.ToLower(strings.TrimSpace(record.Type)))
		}
		if strings.TrimSpace(record.AuthIndex) == ref && validProvider(provider) {
			matches = append(matches, record)
		}
	}
	switch len(matches) {
	case 0:
		return rawCredentialRecord{}, ErrCredentialNotFound
	case 1:
		return matches[0], nil
	default:
		return rawCredentialRecord{}, ErrCredentialConflict
	}
}

func (c *ManagementClient) removeAPIKeyCredential(ctx context.Context, record rawCredentialRecord) error {
	provider := Provider(strings.ToLower(strings.TrimSpace(record.Provider)))
	if provider == "" {
		provider = Provider(strings.ToLower(strings.TrimSpace(record.Type)))
	}
	if !validProvider(provider) {
		return ErrUnsupportedProvider
	}
	endpoint, field := providerKeyEndpoint(provider)
	items, err := c.readRawKeyList(ctx, endpoint, field)
	if err != nil {
		return err
	}
	ref := strings.TrimSpace(record.AuthIndex)
	kept := make([]json.RawMessage, 0, len(items))
	matches := 0
	for _, item := range items {
		var identity struct {
			AuthIndex string `json:"auth-index"`
		}
		if json.Unmarshal(item, &identity) != nil {
			return ErrInvalidResponse
		}
		if strings.TrimSpace(identity.AuthIndex) == ref {
			matches++
			continue
		}
		kept = append(kept, item)
	}
	if matches == 0 {
		return nil
	}
	if matches > 1 {
		return ErrCredentialConflict
	}
	payload, err := json.Marshal(kept)
	if err != nil {
		return ErrInvalidResponse
	}
	var response map[string]any
	return mapCredentialOperationError(c.doJSON(ctx, "remove API key", http.MethodPut, endpoint, json.RawMessage(payload), &response))
}

func mapCredentialOperationError(err error) error {
	if err == nil {
		return nil
	}
	var statusErr *ManagementStatusError
	if !errors.As(err, &statusErr) {
		return err
	}
	switch statusErr.StatusCode {
	case http.StatusNotFound:
		return ErrCredentialNotFound
	case http.StatusConflict:
		return ErrCredentialConflict
	case http.StatusNotImplemented:
		return ErrOperationUnsupported
	default:
		return err
	}
}
