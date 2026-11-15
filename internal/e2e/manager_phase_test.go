package e2e

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/store"
)

func managerPhaseFailure(t *testing.T, s *server, parent context.Context, issueID, phase string, cause error) {
	t.Helper()
	t.Logf("manager phase=%q issue=%s cause=%v pool=%+v", phase, issueID, cause, s.store.PoolStats())
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		for _, query := range []string{
			`SELECT json_object('run',run_id,'role',role,'state',observed_state) FROM runs`,
			`SELECT json_object('run',run_id,'lease',status) FROM leases`,
			`SELECT json_object('request',request_id,'status',status) FROM outbox WHERE destination LIKE 'background_thread:%'`,
		} {
			rows, err := tx.Q().QueryContext(ctx, query)
			if err != nil {
				return err
			}
			for rows.Next() {
				var value string
				if err := rows.Scan(&value); err != nil {
					rows.Close()
					return err
				}
				t.Log(value)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Logf("manager diagnostic snapshot incomplete: %v", err)
	}
	stack := make([]byte, 1<<20)
	n := runtime.Stack(stack, true)
	t.Logf("manager phase goroutines:\n%s", stack[:n])
	t.Fatalf("manager phase %q failed for original issue %s: %v", phase, issueID, cause)
}
