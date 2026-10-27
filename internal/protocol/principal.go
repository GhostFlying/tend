package protocol

import "fmt"

// PrincipalKind classifies the authenticated caller of a request.
type PrincipalKind string

// Principal kinds.
const (
	// PrincipalOwner is the human user, identified by tailnet login.
	PrincipalOwner PrincipalKind = "owner"
	// PrincipalMainAgent is a main-agent thread inside a harness instance.
	PrincipalMainAgent       PrincipalKind = "main_agent"
	PrincipalBackgroundAgent PrincipalKind = "background_agent"
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
	Kind           PrincipalKind `json:"kind"`
	ConversationID string        `json:"conversation_id,omitempty"`
	// Subject identifies the caller within its kind: the login for owners,
	// the harness instance for the hosted main agent, the client ID of the
	// token for agents the owner works in, the host for runners and the run
	// ID for runs. It is empty for the system principal.
	Subject string `json:"subject,omitempty"`
	// Thread is the harness thread for hosted main-agent callers, and the
	// agent's session, when known, for agents the owner works in.
	Thread string `json:"thread,omitempty"`
	// Agent is the kind of agent for main-agent callers that are agents the
	// owner works in (D45); it is empty for the hosted main agent.
	Agent AgentKind `json:"agent,omitempty"`
	// Role is the run role for run callers.
	Role RunRole `json:"role,omitempty"`
	// Client identifies the client instance, such as one browser on one
	// device, that an owner request came from. Several clients of the same
	// owner may be active at once. It is used for auditing and per-client
	// state like event cursors; it is not a credential and does not affect
	// the dedup domain.
	Client string `json:"client,omitempty"`
}

// Validate checks that the principal carries the fields its kind needs.
func (p Principal) Validate() error {
	switch p.Kind {
	case PrincipalOwner:
		if p.Subject == "" || p.Client == "" {
			return fmt.Errorf("protocol: owner principal needs a subject and a client")
		}
	case PrincipalRunner:
		if p.Subject == "" {
			return fmt.Errorf("protocol: %s principal needs a subject", p.Kind)
		}
	case PrincipalMainAgent:
		// The hosted main agent always has its thread. An agent the owner
		// works in may not know its session.
		if p.Subject == "" || (p.Agent == "" && p.Thread == "") || (p.Agent != "" && !p.Agent.Valid()) {
			return fmt.Errorf("protocol: main_agent principal needs a subject and a thread or a known agent kind")
		}
	case PrincipalBackgroundAgent:
		if p.Subject == "" || p.Thread == "" || p.ConversationID == "" || p.Agent != "" {
			return fmt.Errorf("protocol: background principal requires a registered conversation and thread")
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
// unique. All clients of the owner share one domain, so any client can look
// up a request another client sent; request IDs are random, so clients do not
// collide. Each harness instance, host and run has its own domain.
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

// Addon reports whether p is an agent the owner works in, rather than the
// hosted main agent.
func (p Principal) Addon() bool { return p.Kind == PrincipalMainAgent && p.Agent != "" }
