package mainthread

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
)

type managerReplayReadFixture struct {
	t        *testing.T
	errorAt  string
	readErr  error
	readOnly []string
	batch    []app.ManagerReplayEvent
}

func (f *managerReplayReadFixture) observe(run, step string) error {
	f.t.Helper()
	if run != "original" {
		f.t.Fatal("manager replay changed Run", run)
	}
	f.readOnly = append(f.readOnly, step)
	if step == f.errorAt {
		f.errorAt = ""
		return f.readErr
	}
	return nil
}

func (f *managerReplayReadFixture) ManagerReplayEvents(_ context.Context, run string, after int64) ([]app.ManagerReplayEvent, error) {
	if after != 88 {
		f.t.Fatal("manager replay advanced its cursor after a failed read", after)
	}
	return f.batch, f.observe(run, "events")
}

func (f *managerReplayReadFixture) ManagerTransportEnded(_ context.Context, run string) (bool, error) {
	return false, f.observe(run, "transport")
}

func (f *managerReplayReadFixture) ManagerRunEnded(_ context.Context, run string) (bool, error) {
	return false, f.observe(run, "run")
}

func TestManagerEventReadRetriesTheSameBatch(t *testing.T) {
	for _, stage := range []string{"events", "transport", "run"} {
		t.Run(stage, func(t *testing.T) {
			f := &managerReplayReadFixture{t: t, errorAt: stage, readErr: context.DeadlineExceeded}
			want := []string{"events", "transport", "events", "transport"}
			if stage != "transport" {
				f.batch = []app.ManagerReplayEvent{{Cursor: 90, RunID: "original", Sequence: 12}, {Cursor: 92, RunID: "original", Sequence: 13}}
				want = []string{"events", "events", "run"}
				if stage == "run" {
					want = []string{"events", "run", "events", "run"}
				}
			}
			events, ended, err := readManagerEventBatch(t.Context(), f, "original", 88)
			if err != nil || ended || len(events) != len(f.batch) {
				t.Fatal("manager replay did not recover", events, ended, err)
			}
			for i, event := range events {
				if event.Cursor != f.batch[i].Cursor || event.RunID != f.batch[i].RunID || event.Sequence != f.batch[i].Sequence {
					t.Fatal("manager replay changed event identity or order", events)
				}
			}
			if !slices.Equal(f.readOnly, want) {
				t.Fatal("manager replay changed read sequence", f.readOnly, "want", want)
			}
		})
	}
}

func TestManagerEventReadPreservesPermanentFailure(t *testing.T) {
	corrupt := errors.New("invalid event")
	f := &managerReplayReadFixture{t: t, errorAt: "events", readErr: corrupt}
	_, _, err := readManagerEventBatch(t.Context(), f, "original", 88)
	if !errors.Is(err, corrupt) || len(f.readOnly) != 1 {
		t.Fatal("manager replay hid a permanent read failure", err, f.readOnly)
	}
}
