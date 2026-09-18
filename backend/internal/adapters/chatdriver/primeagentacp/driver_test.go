package primeagentacp

import (
	"context"
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureUsesACPMode(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--mode", "acp"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
}

func TestSessionOptionsMapModelAndEffort(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); len(got) != 0 {
		t.Fatalf("empty settings = %#v", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{Model: "anthropic:claude-sonnet-4", Effort: "high"})
	want := []acpdriver.SessionOption{
		{ID: "model", Value: "anthropic:claude-sonnet-4"},
		{ID: "thought_level", Value: "high"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}

func TestSessionOptionsOmitBlankEffort(t *testing.T) {
	got := sessionOptions(ports.ChatTurnSettings{Model: "model-1"})
	want := []acpdriver.SessionOption{{ID: "model", Value: "model-1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}
