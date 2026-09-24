package domain

import (
	"errors"
	"time"
)

var ErrResearchAlreadyRunning = errors.New("research is already running for this orchestrator")

// ResearchRun is one orchestrator-requested repository investigation.
type ResearchRun struct {
	ID              string            `json:"id"`
	ParentSessionID SessionID         `json:"parentSessionId"`
	ProjectID       ProjectID         `json:"projectId"`
	Prompt          string            `json:"prompt"`
	Harness         AgentHarness      `json:"agent"`
	AgentConfig     AgentConfig       `json:"agentConfig"`
	Status          string            `json:"status"`
	Result          string            `json:"result,omitempty"`
	Error           string            `json:"error,omitempty"`
	Approval        *ResearchApproval `json:"approval,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	StartedAt       *time.Time        `json:"startedAt,omitempty"`
	FinishedAt      *time.Time        `json:"finishedAt,omitempty"`
}

type ResearchApproval struct {
	RequestID string                   `json:"requestId"`
	Summary   string                   `json:"summary"`
	Options   []ResearchApprovalOption `json:"options"`
}

type ResearchApprovalOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}
