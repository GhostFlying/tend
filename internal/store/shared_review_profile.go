package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

func SharedReviewExecutionRequirements(ctx context.Context, q Querier, r protocol.SharedDeliveryReview) (*protocol.SharedReviewExecutionRequirements, error) {
	out := &protocol.SharedReviewExecutionRequirements{Profiles: []protocol.SharedReviewExecutionProfile{}}
	for _, link := range r.Links {
		if link.TaskID == "" {
			continue
		}
		task, err := GetTask(ctx, q, link.TaskID)
		if err != nil {
			return nil, err
		}
		raw, err := GetSpec(ctx, q, task.TaskID, task.SpecVersion)
		if err != nil {
			return nil, err
		}
		var spec struct {
			AgentSelection      *protocol.TaskAgentSelection `json:"agent_selection"`
			AcceptanceExecution *protocol.ExecutionProfile   `json:"acceptance_execution"`
		}
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			return nil, err
		}
		if spec.AgentSelection != nil {
			required := spec.AgentSelection.Acceptance
			if required.Model != "" || required.Effort != "" {
				out.Profiles = append(out.Profiles, protocol.SharedReviewExecutionProfile{Model: required.Model, Effort: required.Effort})
			}
		}
		if p := spec.AcceptanceExecution; p != nil {
			out.Profiles = append(out.Profiles, protocol.SharedReviewExecutionProfile{Model: p.Model, Effort: p.Effort, Exact: true})
		}
	}
	return out, nil
}

func RequireSharedReviewExecutionProfile(requirements *protocol.SharedReviewExecutionRequirements, profile protocol.ExecutionProfile) error {
	for _, required := range requirements.Profiles {
		matches := (required.Model == "" || required.Model == profile.Model) && (required.Effort == "" || required.Effort == profile.Effort)
		if required.Exact {
			matches = required.Model == profile.Model && required.Effort == profile.Effort
		}
		if !matches {
			return protocol.Errorf(protocol.CodeForbidden, "the actual formal reviewer execution must satisfy every linked task's model and reasoning effort requirements")
		}
	}
	return nil
}

func sharedReviewExecutionCurrent(ctx context.Context, q Querier, r protocol.SharedDeliveryReview) (bool, error) {
	requirements, err := SharedReviewExecutionRequirements(ctx, q, r)
	if err != nil {
		return false, err
	}
	if len(r.Rounds) == 0 {
		return len(requirements.Profiles) == 0, nil
	}
	round := r.Rounds[len(r.Rounds)-1]
	if len(requirements.Profiles) == 0 && (round.ExecutionRequirements == nil || len(round.ExecutionRequirements.Profiles) == 0) {
		return true, nil
	}
	run := round.RunID
	if round.SupplementRunID != "" {
		run = round.SupplementRunID
	}
	pin, err := GetRunExecution(ctx, q, run)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if round.ExecutionRequirements != nil && RequireSharedReviewExecutionProfile(round.ExecutionRequirements, pin.Profile) != nil {
		return false, nil
	}
	return RequireSharedReviewExecutionProfile(requirements, pin.Profile) == nil, nil
}
