package journal

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRetainedResumePermissionSurvivesRestartWithoutChangingAssignment(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "journal.db")
	now := func() time.Time { return time.UnixMilli(1000) }
	j, err := Open(ctx, path, now)
	if err != nil {
		t.Fatal(err)
	}
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "original", LeaseID: "lease", TaskID: "task", Role: protocol.RoleImplement}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, "original-opaque-thread"); err != nil {
		t.Fatal(err)
	}
	if err := j.SetFenced(ctx, as.RunID, true); err != nil {
		t.Fatal(err)
	}
	before, err := j.Run(ctx, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	authority := protocol.RetainedRunResume{RequestID: "independent-permission", Mode: "attach"}
	if err := j.SetRetainedResume(ctx, as.RunID, authority); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(ctx, path, now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	saved, err := j.RetainedResume(ctx, as.RunID)
	if err != nil || saved == nil || *saved != authority {
		t.Fatal("strict permission disappeared after restart", saved, err)
	}
	after, err := j.Run(ctx, as.RunID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("strict permission rewrote original assignment, thread or fence", after, err)
	}
}
