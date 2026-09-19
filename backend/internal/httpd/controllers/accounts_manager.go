package controllers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/accountsmanager"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

type AccountsManagerStatusSource interface {
	Status() accountsmanager.Status
}

// AccountsManagerController exposes only the safe capability state. Process
// coordinates and authentication material remain daemon-internal.
type AccountsManagerController struct {
	Status AccountsManagerStatusSource
}

func (c *AccountsManagerController) Register(r chi.Router) {
	r.Get("/accounts-manager/status", c.getStatus)
}

func (c *AccountsManagerController) getStatus(w http.ResponseWriter, r *http.Request) {
	if c.Status == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/accounts-manager/status")
		return
	}
	status := c.Status.Status()
	var reason *string
	if status.Reason != "" {
		value := string(status.Reason)
		reason = &value
	}
	envelope.WriteJSON(w, http.StatusOK, AccountsManagerStatusResponse{
		State:         string(status.State),
		Reason:        reason,
		EngineVersion: status.EngineVersion,
	})
}
