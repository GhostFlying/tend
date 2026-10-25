package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

type relationQueryCounter struct {
	store.Querier
	total, queueScans int
}

func (q *relationQueryCounter) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.total++
	if strings.Contains(query, "FROM plan_actions a") {
		q.queueScans++
	}
	return q.Querier.QueryContext(ctx, query, args...)
}

func (q *relationQueryCounter) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.total++
	return q.Querier.QueryRowContext(ctx, query, args...)
}

func TestTaskRelationsBatchExecutionFactsInsideClaimTransaction(t *testing.T) {
	h := newHarness(t)
	_, root := h.createTask(owner, createTaskPayload{Title: "shared root", Spec: testSpec})
	const relatedCount = 150
	outgoing := []protocol.TaskRelation{}
	titles := map[string]string{}
	for i := 0; i < relatedCount; i++ {
		payload := createTaskPayload{Title: fmt.Sprintf("related %d", i), Spec: testSpec}
		if i%2 == 0 {
			payload.Relations = []protocol.TaskRelation{{Kind: protocol.RelationRelated, TaskID: root.TaskID}}
		}
		op, task := h.createTask(owner, payload)
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		titles[task.TaskID] = payload.Title
		if i%2 != 0 {
			outgoing = append(outgoing, protocol.TaskRelation{Kind: protocol.RelationRelated, TaskID: task.TaskID})
		}
	}
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(root.TaskID), updateIntentPayload{Relations: &outgoing}, rev(h.task(root.TaskID).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// Assignment construction calls this view in Claim's writer transaction.
	// Count database work there rather than asserting machine-dependent timings.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		expected, err := store.ListTaskRelations(t.Context(), tx.Q(), root.TaskID)
		if err != nil {
			return err
		}
		counter := &relationQueryCounter{Querier: tx.Q()}
		relations, err := views.TaskRelations(t.Context(), counter, root.TaskID)
		if err != nil {
			return err
		}
		if len(relations) != relatedCount || len(expected) != relatedCount {
			t.Fatalf("relations %d, want %d", len(relations), relatedCount)
		}
		if counter.queueScans != 1 {
			t.Fatalf("%d relations caused %d whole-queue scans, want one", relatedCount, counter.queueScans)
		}
		// Leave room for linear per-task reads while catching repeated traversal of
		// all pending tasks even if the candidate query is cached independently.
		if counter.total > relatedCount*12+20 {
			t.Fatalf("nonlinear related-task read: %d queries for %d tasks", counter.total, relatedCount)
		}
		t.Logf("%d relations: %d SQL queries, %d whole-queue scan", relatedCount, counter.total, counter.queueScans)
		for i, relation := range relations {
			row := expected[i]
			id, direction := row.TargetTaskID, protocol.RelationOutgoing
			if row.TargetTaskID == root.TaskID {
				id, direction = row.SourceTaskID, protocol.RelationIncoming
			}
			if relation.Task.TaskID != id || relation.Task.Title != titles[id] || relation.Direction != direction || relation.Kind != row.Kind {
				t.Fatalf("relation %d lost order or identity: %+v, source %+v", i, relation, row)
			}
			if relation.Task.Execution == nil || relation.Task.Execution.State != protocol.ExecutionQueued {
				t.Fatalf("relation %d lost execution facts: %+v", i, relation)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	as, claimed, _ := h.claim("linux-1")
	if !claimed || as.TaskID != root.TaskID || len(as.Relations) != relatedCount {
		t.Fatalf("assignment relation context: task %s, count %d", as.TaskID, len(as.Relations))
	}
}
