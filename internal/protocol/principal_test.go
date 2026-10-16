package protocol

import (
	"errors"
	"fmt"
	"testing"
)

func TestPrincipalValidate(t *testing.T) {
	tests := []struct {
		name    string
		p       Principal
		wantErr bool
	}{
		{"owner", Principal{Kind: PrincipalOwner, Subject: "user@example.com", Client: "cli-phone"}, false},
		{"owner without subject", Principal{Kind: PrincipalOwner, Client: "cli-phone"}, true},
		{"owner without client", Principal{Kind: PrincipalOwner, Subject: "user@example.com"}, true},
		{"main agent", Principal{Kind: PrincipalMainAgent, Subject: "harness-1", Thread: "th-1"}, false},
		{"main agent without thread", Principal{Kind: PrincipalMainAgent, Subject: "harness-1"}, true},
		{"add-on agent", Principal{Kind: PrincipalMainAgent, Subject: "cli-0123456789", Agent: AgentCodex, Thread: "sess-1"}, false},
		{"add-on agent without session", Principal{Kind: PrincipalMainAgent, Subject: "cli-0123456789", Agent: AgentClaude}, false},
		{"add-on agent of unknown kind", Principal{Kind: PrincipalMainAgent, Subject: "cli-0123456789", Agent: "cursor"}, true},
		{"add-on agent without subject", Principal{Kind: PrincipalMainAgent, Agent: AgentTraex}, true},
		{"runner", Principal{Kind: PrincipalRunner, Subject: "host-a"}, false},
		{"run", Principal{Kind: PrincipalRun, Subject: "run_x", Role: RoleAccept}, false},
		{"run with bad role", Principal{Kind: PrincipalRun, Subject: "run_x", Role: "judge"}, true},
		{"system", Principal{Kind: PrincipalSystem}, false},
		{"unknown kind", Principal{Kind: "guest", Subject: "x"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.p.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPrincipalDedupDomain(t *testing.T) {
	tests := []struct {
		p    Principal
		want string
	}{
		{Principal{Kind: PrincipalOwner, Subject: "user@example.com", Client: "cli-phone"}, "owner"},
		{Principal{Kind: PrincipalOwner, Subject: "user@example.com", Client: "cli-laptop"}, "owner"},
		{Principal{Kind: PrincipalMainAgent, Subject: "harness-1", Thread: "th-1"}, "main_agent:harness-1"},
		{Principal{Kind: PrincipalMainAgent, Subject: "harness-1", Thread: "th-2"}, "main_agent:harness-1"},
		{Principal{Kind: PrincipalRunner, Subject: "host-a"}, "runner:host-a"},
		{Principal{Kind: PrincipalRun, Subject: "run_x", Role: RoleImplement}, "run:run_x"},
		{Principal{Kind: PrincipalSystem}, "system"},
	}
	for _, tt := range tests {
		if got := tt.p.DedupDomain(); got != tt.want {
			t.Errorf("%+v.DedupDomain() = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestCodeOf(t *testing.T) {
	wrapped := fmt.Errorf("apply: %w", Errorf(CodeStaleRevision, "revision %d is stale", 3))
	if got := CodeOf(wrapped); got != CodeStaleRevision {
		t.Errorf("CodeOf(wrapped) = %q, want %q", got, CodeStaleRevision)
	}
	if got := CodeOf(errors.New("disk full")); got != CodeInternal {
		t.Errorf("CodeOf(plain error) = %q, want %q", got, CodeInternal)
	}
}
