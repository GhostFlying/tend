package mainthread

import (
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestGenericReceiptOnlyResumesOriginalWithoutBootstrap(t *testing.T) {
	for _, maintenance := range []bool{false, true} {
		for _, mode := range []string{"old_toolset", "rejected_resume", "missing_binding"} {
			name := mode
			if maintenance {
				name += "_maintenance"
			}
			t.Run(name, func(t *testing.T) {
				e := newEnv(t, testkit.NewHarness())
				e.stop()
				selected := receiptOnlySelection()
				selected.RequireReceiptReader = false
				selected.NativeOwnerTools = true
				// Generic readers keep their existing model contract, without imposing
				// the strict reader's separate proof or native catalog requirements.
				selected.AppliedModel, selected.AppliedProvider, selected.AppliedEffort = "", "", ""
				e.m.named = selected
				e.m.cfg.MaintenanceEnabled = maintenance
				if mode != "missing_binding" {
					if _, err := e.app.BindMainThread(t.Context(), e.m.bindingInstance(), "original-thread", "old-toolset", "original-history"); err != nil {
						t.Fatal(err)
					}
				}
				original := &receiptThread{Thread: &historyReference{id: "original-thread", path: "original-history"}}
				session := &receiptOnlySession{thread: original}
				if mode == "rejected_resume" {
					session.resumeErr = harness.ErrRejected
				}
				thread, err := e.m.openThread(t.Context(), session)
				switch mode {
				case "old_toolset":
					if err != nil || thread != original {
						t.Fatal("generic reader changed original bootstrap", thread, err)
					}
				case "rejected_resume":
					if !errors.Is(err, harness.ErrRejected) {
						t.Fatal("original resume rejection replaced", err)
					}
				case "missing_binding":
					if err == nil {
						t.Fatal("missing original binding treated as ready")
					}
				}
				resumes := 1
				if mode == "missing_binding" {
					resumes = 0
				}
				if session.starts != 0 || session.resumes != resumes || len(e.h.Starts()) != 0 || e.count(`SELECT COUNT(*) FROM main_thread_handoffs`) != 0 {
					t.Fatal("generic receipt reader created/migrated native work", session.starts, session.resumes)
				}
			})
		}
	}
}
