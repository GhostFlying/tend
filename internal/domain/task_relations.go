package domain

import (
	"cmp"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

// NormalizeTaskRelations validates declarations and gives them a stable order.
func NormalizeTaskRelations(taskID string, refs []protocol.TaskRelation) ([]protocol.TaskRelation, error) {
	out := slices.Clone(refs)
	for _, r := range out {
		if !protocol.ValidID(protocol.PrefixTask, r.TaskID) || r.TaskID == taskID {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "relations must reference another valid task")
		}
		if r.Kind != protocol.RelationFollowUp && r.Kind != protocol.RelationRelated {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "unknown task relation kind %q", r.Kind)
		}
	}
	slices.SortFunc(out, func(a, b protocol.TaskRelation) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.TaskID, b.TaskID))
	})
	for i := 1; i < len(out); i++ {
		if out[i] == out[i-1] {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "duplicate task relation")
		}
	}
	return out, nil
}

// UpdateRelations changes metadata even on a terminal task, without producing
// work or changing its spec, completion evidence or lifecycle.
func UpdateRelations(t Task, refs, current []protocol.TaskRelation, expected int64) (Decision, error) {
	if err := checkRevision(t, expected); err != nil {
		return Decision{}, err
	}
	next, err := NormalizeTaskRelations(t.ID, refs)
	if err != nil {
		return Decision{}, err
	}
	prev, err := NormalizeTaskRelations(t.ID, current)
	if err != nil {
		return Decision{}, err
	}
	if slices.Equal(next, prev) {
		return Decision{Task: t}, nil
	}
	t.ControlRevision++
	return Decision{Task: t, Events: []EventDraft{{Kind: "task.relations_updated", Body: map[string]any{"relations": next}}}}, nil
}
