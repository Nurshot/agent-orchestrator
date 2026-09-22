package runner

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coresession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
)

const (
	routeTokenPrefix        = "ao-route-v1."
	routeAccessProviderType = "ao-route-capability"
	routeAccessProviderName = "ao-route"
)

var errPinnedAccountUnavailable = errors.New("pinned account is unavailable")

type routeClaims struct {
	Version   int    `json:"v"`
	Provider  string `json:"provider"`
	AuthIndex string `json:"auth_index"`
	SessionID string `json:"session_id"`
}

type routeCapability struct {
	aead   cipher.AEAD
	claims sync.Map // caller scope -> routeClaimsEntry
}

type routeClaimsEntry struct {
	claims   routeClaims
	lastSeen time.Time
}

func newRouteCapability(key []byte) (*routeCapability, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("routing key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize route capability: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize route capability: %w", err)
	}
	return &routeCapability{aead: aead}, nil
}

func (c *routeCapability) Identifier() string { return routeAccessProviderName }

func (c *routeCapability) Mint(claims routeClaims) (string, error) {
	claims.Provider = strings.ToLower(strings.TrimSpace(claims.Provider))
	claims.AuthIndex = strings.TrimSpace(claims.AuthIndex)
	claims.SessionID = strings.TrimSpace(claims.SessionID)
	if claims.Provider != "codex" && claims.Provider != "claude" || claims.AuthIndex == "" || claims.SessionID == "" {
		return "", fmt.Errorf("invalid route capability claims")
	}
	claims.Version = 1
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode route capability: %w", err)
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate route capability: %w", err)
	}
	sealed := c.aead.Seal(nil, nonce, payload, []byte(routeTokenPrefix))
	encoded := append(nonce, sealed...)
	return routeTokenPrefix + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func (c *routeCapability) Authenticate(_ context.Context, request *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	token := routeTokenFromRequest(request)
	if !strings.HasPrefix(token, routeTokenPrefix) {
		return nil, sdkaccess.NewNotHandledError()
	}
	claims, err := c.open(token)
	if err != nil {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	sum := sha256.Sum256([]byte(token))
	principal := routeAccessProviderName + ":" + hex.EncodeToString(sum[:])
	now := time.Now()
	c.claims.Range(func(key, value any) bool {
		entry, ok := value.(routeClaimsEntry)
		if !ok || now.Sub(entry.lastSeen) > 10*time.Minute {
			c.claims.Delete(key)
		}
		return true
	})
	c.claims.Store(coresession.CallerScope(principal), routeClaimsEntry{claims: claims, lastSeen: now})
	return &sdkaccess.Result{Provider: routeAccessProviderName, Principal: principal}, nil
}

func (c *routeCapability) open(token string) (routeClaims, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, routeTokenPrefix))
	if err != nil || len(encoded) <= c.aead.NonceSize() {
		return routeClaims{}, errors.New("invalid route capability")
	}
	nonce, ciphertext := encoded[:c.aead.NonceSize()], encoded[c.aead.NonceSize():]
	payload, err := c.aead.Open(nil, nonce, ciphertext, []byte(routeTokenPrefix))
	if err != nil {
		return routeClaims{}, errors.New("invalid route capability")
	}
	var claims routeClaims
	if err = json.Unmarshal(payload, &claims); err != nil || claims.Version != 1 {
		return routeClaims{}, errors.New("invalid route capability")
	}
	claims.Provider = strings.ToLower(strings.TrimSpace(claims.Provider))
	claims.AuthIndex = strings.TrimSpace(claims.AuthIndex)
	claims.SessionID = strings.TrimSpace(claims.SessionID)
	if claims.Provider != "codex" && claims.Provider != "claude" || claims.AuthIndex == "" || claims.SessionID == "" {
		return routeClaims{}, errors.New("invalid route capability")
	}
	return claims, nil
}

func routeTokenFromRequest(request *http.Request) string {
	if request == nil {
		return ""
	}
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if parts := strings.Fields(authorization); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(request.Header.Get("x-api-key"))
}

type exactRouteSelector struct {
	capability *routeCapability
	fallback   coreauth.Selector
}

func newExactRouteSelector(capability *routeCapability, fallback coreauth.Selector) *exactRouteSelector {
	if fallback == nil {
		fallback = &coreauth.FillFirstSelector{}
	}
	return &exactRouteSelector{capability: capability, fallback: fallback}
}

func (s *exactRouteSelector) Pick(ctx context.Context, provider, model string, opts coreexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	scope, _ := opts.Metadata[coreexecutor.CallerScopeMetadataKey].(string)
	if claimsValue, ok := s.capability.claims.Load(strings.TrimSpace(scope)); ok {
		claims := claimsValue.(routeClaimsEntry).claims
		if !strings.EqualFold(claims.Provider, provider) {
			return nil, errPinnedAccountUnavailable
		}
		for _, auth := range auths {
			if auth != nil && strings.EqualFold(auth.Provider, claims.Provider) && auth.Index == claims.AuthIndex && exactRouteAuthUsable(auth, model, time.Now()) {
				return auth, nil
			}
		}
		return nil, errPinnedAccountUnavailable
	}
	return s.fallback.Pick(ctx, provider, model, opts, auths)
}

func exactRouteAuthUsable(auth *coreauth.Auth, model string, now time.Time) bool {
	if auth == nil || auth.Disabled || auth.Unavailable || auth.Status != coreauth.StatusActive || (!auth.NextRetryAfter.IsZero() && auth.NextRetryAfter.After(now)) {
		return false
	}
	for key, state := range auth.ModelStates {
		if state == nil || !strings.EqualFold(strings.TrimSpace(key), strings.TrimSpace(model)) {
			continue
		}
		return !state.Unavailable && state.Status == coreauth.StatusActive && (state.NextRetryAfter.IsZero() || !state.NextRetryAfter.After(now))
	}
	return true
}
