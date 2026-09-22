package runner

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type routeTokenHandler struct {
	managementKey   string
	baseURL         string
	capability      *routeCapability
	credentialAlive func(provider, authIndex string) bool
}

func newRouteTokenHandler(managementKey, baseURL string, capability *routeCapability, credentialAlive func(string, string) bool) http.Handler {
	return &routeTokenHandler{
		managementKey: managementKey, baseURL: strings.TrimRight(baseURL, "/"), capability: capability, credentialAlive: credentialAlive,
	}
}

func (h *routeTokenHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !validControlAuthorization(r.Header.Get("Authorization"), h.managementKey) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/ao/internal/routes/token" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var input struct {
		Provider  string `json:"provider"`
		AuthIndex string `json:"authIndex"`
		SessionID string `json:"sessionId"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeRouteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.AuthIndex = strings.TrimSpace(input.AuthIndex)
	input.SessionID = strings.TrimSpace(input.SessionID)
	if input.Provider != "codex" && input.Provider != "claude" || input.AuthIndex == "" || input.SessionID == "" {
		writeRouteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.credentialAlive == nil || !h.credentialAlive(input.Provider, input.AuthIndex) {
		writeRouteError(w, http.StatusNotFound, "account_unavailable")
		return
	}
	token, err := h.capability.Mint(routeClaims{Provider: input.Provider, AuthIndex: input.AuthIndex, SessionID: input.SessionID})
	if err != nil {
		writeRouteError(w, http.StatusInternalServerError, "route_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"baseUrl": h.baseURL, "token": token})
}

func writeRouteError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
