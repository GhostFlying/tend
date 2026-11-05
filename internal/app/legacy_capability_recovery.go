package app

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Historical evidence-only bindings (absent match_policy) retain their original
// recovery contract and persisted receipts. This is a historical-data reader,
// not a qualification path for new declaration_v1 assignments.
func legacyRunCapabilityMatch(ctx context.Context, q store.Querier, b protocol.RunCapabilityBinding, r protocol.RunnerRuntime, now int64) (protocol.CapabilityMatch, error) {
	all, err := store.AgentCapabilityEvidence(ctx, q, b.Pin.Identity.ExecutionEnvironmentID, b.Pin.Identity.LocalID)
	if err != nil {
		return protocol.CapabilityMatch{}, err
	}
	current := []protocol.ExecutorCapabilityEvidence{}
	for _, e := range all {
		if e.Pin == b.Pin && e.InstanceID == r.InstanceID && e.Generation == r.Generation {
			current = append(current, e)
		}
	}
	return legacyEvidenceMatch(b.Pin, b.Required, current, now), nil
}
func recordLegacyCapabilityRevalidations(ctx context.Context, x *Exec, tx *store.Tx, e protocol.ExecutorCapabilityEvidence, r protocol.RunnerRuntime) ([]Event, error) {
	events := []Event{}
	if e.Kind != "tool_inventory" {
		return events, nil
	}
	runs, err := store.ListOpenRunsOnHost(ctx, tx.Q(), x.Principal.Subject)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		b, err := store.GetRunCapabilityBinding(ctx, tx.Q(), run.RunID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if b.MatchPolicy != "" || b.Pin != e.Pin {
			continue
		}
		match, err := legacyRunCapabilityMatch(ctx, tx.Q(), b, r, x.Now.UnixMilli())
		if err != nil {
			return nil, err
		}
		if match.State != "eligible" || !slices.Contains(match.EvidenceIDs, e.EvidenceID) {
			continue
		}
		recovery := protocol.RunCapabilityRevalidation{RequestID: x.Env.RequestID, Pin: b.Pin, RequiredSHA256: b.RequiredSHA256, EvidenceIDs: match.EvidenceIDs, ObservedAtMillis: x.Now.UnixMilli()}
		for _, id := range match.EvidenceIDs {
			fact, err := store.GetCapabilityEvidence(ctx, tx.Q(), id)
			if err != nil {
				return nil, err
			}
			recovery.Evidence = append(recovery.Evidence, fact)
		}
		if err := tx.InsertRunCapabilityRevalidation(ctx, run.RunID, recovery); err != nil {
			return nil, err
		}
		events = append(events, Event{ObjectType: "run", ObjectID: run.RunID, Kind: "run.capability_revalidated", Body: map[string]any{"request_id": recovery.RequestID, "evidence_ids": recovery.EvidenceIDs}})
	}
	return events, nil
}

func legacyEvidenceMatch(pin protocol.RunExecutionPin, required []protocol.CapabilityReference, evidence []protocol.ExecutorCapabilityEvidence, now int64) protocol.CapabilityMatch {
	m := protocol.CapabilityMatch{State: "eligible", Required: slices.Clone(required), Reasons: []protocol.CapabilityReason{}, EvidenceIDs: []string{}, CheckedAtMillis: now}
	for _, ref := range required {
		r := protocol.CapabilityReason{Capability: ref, State: "pending", Reason: "evidence_unverified", Recovery: "verify the exact configured executor once; reconcile any unknown original request"}
		var latest *protocol.ExecutorCapabilityEvidence
		for i := range evidence {
			e := &evidence[i]
			if e.Capability == ref && e.Pin == pin && e.Kind != "execution" && (latest == nil || e.Generation > latest.Generation || e.Generation == latest.Generation && e.Sequence > latest.Sequence) {
				latest = e
			}
		}
		if latest != nil {
			e := *latest
			r.EvidenceID, r.ExpiresAtMillis = e.EvidenceID, e.ExpiresAtMillis
			switch {
			case e.Validate(now) != nil:
				r.Reason = "evidence_invalid"
			case e.ExpiresAtMillis <= now:
				r.Reason = "evidence_expired"
			case e.Kind == "simulation":
				r.Reason = "simulation_only"
			case e.Scope != "profile":
				r.State, r.Reason = "unavailable", "session_adoption_unsupported"
			case e.State == "unavailable":
				r.State, r.Reason = "unavailable", e.Reason
			case e.State == "unverified":
				r.Reason = e.Reason
				if e.Reason == "adapter_evidence_unsupported" {
					r.State = "unavailable"
				}
			case e.State == "confirmed" && e.Kind == "tool_inventory" && e.Reason == "tool_exposed":
				r.State, r.Reason, r.Recovery = "eligible", "tool_exposed", "recheck the same binding before launch"
				m.EvidenceIDs = append(m.EvidenceIDs, e.EvidenceID)
			}
		}
		m.Reasons = append(m.Reasons, r)
		if r.State == "unavailable" {
			m.State = "unavailable"
		} else if r.State == "pending" && m.State == "eligible" {
			m.State = "pending"
		}
	}
	return m
}
