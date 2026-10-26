package gitws

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestOrcaGitCommentUpgradesRetainedDisplayState(t *testing.T) {
	f, provider := newOrcaFixture(t)
	as := f.implement()
	as.Workspace.ID = "ws_git"
	var comments []string
	f.ws.layout = orcaLayout{w: f.ws, orca: &orca.Client{Exec: func(ctx context.Context, dir string, args []string) ([]byte, error) {
		if args[0] == "worktree" && args[1] == "set" {
			for i, arg := range args {
				if arg == "--comment" {
					comments = append(comments, args[i+1])
				}
			}
		}
		return provider.exec(ctx, dir, args)
	}}}
	f.prepare(as)
	st, err := loadState(f.ws.home(as))
	if err != nil {
		t.Fatal(err)
	}
	st.DisplayComment = ""
	if err := saveState(f.ws.home(as), st); err != nil {
		t.Fatal(err)
	}
	f.prepare(as)
	f.prepare(as)
	if len(comments) != 2 || !strings.HasPrefix(comments[1], "tend workspace ws_git\ntend task "+as.TaskID+"\n") {
		t.Fatalf("retained display not upgraded exactly once: %q", comments)
	}
}

func TestOrcaDirectoryCommentTracksStageAndReuse(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	as := protocol.Assignment{TaskID: "task_first", RunID: "run_first", Role: protocol.RoleImplement, Workspace: protocol.AssignmentWorkspace{ID: "ws_directory", Kind: "directory", TaskTitle: "First task"}}
	w := NewOrca(Config{Home: home}, &orca.Client{})
	path := w.home(as)
	st := state{ResourceID: as.Workspace.ID, ProviderID: "folder:owned", DisplayTask: as.TaskID}
	if err := saveState(path, st); err != nil {
		t.Fatal(err)
	}
	var comments []string
	client := &orca.Client{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		result := map[string]any{}
		switch args[0] + " " + args[1] {
		case "folder-workspace list":
			result["folderWorkspaces"] = []orca.FolderWorkspace{{ID: "owned", FolderPath: path}}
		case "folder-workspace update":
			for i, arg := range args {
				if arg == "--comment" {
					comments = append(comments, args[i+1])
				}
			}
		default:
			t.Fatalf("unexpected operation %v", args)
		}
		return json.Marshal(map[string]any{"ok": true, "result": result})
	}}
	w.layout = orcaLayout{w: w, orca: client}
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleImplement, protocol.RoleAccept} {
		as.Role = role
		if _, err := w.Prepare(ctx, as); err != nil {
			t.Fatal(err)
		}
	}
	as.TaskID, as.RunID, as.Role = "task_followup", "run_followup", protocol.RoleImplement
	as.Workspace.Reused = true
	if _, err := w.Prepare(ctx, as); err != nil {
		t.Fatal(err)
	}
	if len(comments) != 3 {
		t.Fatalf("display updates = %q", comments)
	}
	for _, comment := range comments {
		if !strings.HasPrefix(comment, "tend workspace ws_directory\n") {
			t.Fatalf("missing stable resource marker: %q", comment)
		}
	}
	if !strings.Contains(comments[1], "role accept") || !strings.Contains(comments[2], "tend task task_followup\n") {
		t.Fatalf("stale stage or task in comments: %q", comments)
	}
}
