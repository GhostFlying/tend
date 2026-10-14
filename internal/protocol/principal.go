package protocol

import "fmt"

// PrincipalKind classifies the authenticated caller of a request.
type PrincipalKind string

// Principal kinds.
const (
	// PrincipalOwner is the human user, identified by tailnet login.
	PrincipalOwner PrincipalKind = "owner"
	// PrincipalMainAgent is a main-agent thread inside a harness instance.
	PrincipalMainAgent PrincipalKind = "main_agent"
	// PrincipalRunner is a runner process on a host.
	PrincipalRunner PrincipalKind = "runner"
	// PrincipalRun is a single run acting with a run-scoped credential.
	PrincipalRun PrincipalKind = "run"
	// PrincipalSystem is tend itself, for requests it derives internally.
	PrincipalSystem PrincipalKind = "system"
)

// Principal is the caller identity established by the authentication layer.
// It is never read from a request payload.
type Principal struct {
	Kind PrincipalKind `json:"kind"`
	// Subject identifies the caller within its kind: the login for owners,
	// the harness instance for main agents, the host for runners and the run
	// ID for runs. It is empty for the system principal.
	Subject string `json:"subject,omitempty"`
	// Thread is the harness thread for main-agent callers.
	Thread string `json:"thread,omitempty"`
	// Role is the run role for run callers.
	Role RunRole `json:"role,omitempty"`
}

// Validate checks that the principal carries the fields its kind needs.
func (p Principal) Validate() error {
	switch p.Kind {
	case PrincipalOwner, PrincipalRunner:
		if p.Subject == "" {
			return fmt.Errorf("protocol: %s principal needs a subject", p.Kind)
		}
	case PrincipalMainAgent:
		if p.Subject == "" || p.Thread == "" {
			return fmt.Errorf("protocol: main_agent principal needs a subject and a thread")
		}
	case PrincipalRun:
		if p.Subject == "" || !p.Role.Valid() {
			return fmt.Errorf("protocol: run principal needs a subject and a valid role")
		}
	case PrincipalSystem:
	default:
		return fmt.Errorf("protocol: unknown principal kind %q", p.Kind)
	}
	return nil
}

// DedupDomain returns the namespace in which the caller's request IDs must be
// unique. Owners share one domain across devices so a retry from another
// client still deduplicates; each harness instance, host and run has its own.
func (p Principal) DedupDomain() string {
	switch p.Kind {
	case PrincipalOwner:
		return "owner"
	case PrincipalSystem:
		return "system"
	default:
		return string(p.Kind) + ":" + p.Subject
	}
}
