package gitws

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCleanupRequiresOriginalProviderAfterHarnessChange(t *testing.T) {
	w := New(Config{Home: t.TempDir()})
	ctx := context.Background()
	as := protocol.Assignment{TaskID: "task", Role: protocol.RoleImplement, Workspace: protocol.AssignmentWorkspace{ID: "ws_provider", Kind: "directory"}}
	p, err := w.Prepare(ctx, as)
	if err != nil {
		t.Fatal(err)
	}
	st, err := loadState(p.Dir)
	if err != nil {
		t.Fatal(err)
	}
	st.ProviderID = "folder:original"
	if err := saveState(p.Dir, st); err != nil {
		t.Fatal(err)
	}
	if err := w.Cleanup(ctx, protocol.WorkspaceCleanup{WorkspaceID: as.Workspace.ID, Kind: "directory", Path: p.Dir, ProviderID: st.ProviderID}); err == nil {
		t.Fatal("cleanup bypassed original provider")
	}
	if _, err := os.Stat(p.Dir); err != nil {
		t.Fatal("provider directory was deleted")
	}
}

func TestCleanupPreservesPathRegisteredToReplacementProvider(t *testing.T) {
	for _, replaceAfterRemoval := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement before cleanup", true: "replacement during removal"}[replaceAfterRemoval], func(t *testing.T) {
			w := New(Config{Home: t.TempDir()})
			ctx := context.Background()
			as := protocol.Assignment{TaskID: "task", Role: protocol.RoleImplement, Workspace: protocol.AssignmentWorkspace{ID: "ws_provider", Kind: "directory"}}
			p, err := w.Prepare(ctx, as)
			if err != nil {
				t.Fatal(err)
			}
			st, err := loadState(p.Dir)
			if err != nil {
				t.Fatal(err)
			}
			st.ProviderID = "folder:original"
			if err := saveState(p.Dir, st); err != nil {
				t.Fatal(err)
			}
			originalPresent := replaceAfterRemoval
			removed := 0
			c := &orca.Client{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
				result := map[string]any{}
				switch args[0] + " " + args[1] {
				case "folder-workspace list":
					id := "replacement"
					if originalPresent {
						id = "original"
					}
					result["folderWorkspaces"] = []orca.FolderWorkspace{{ID: id, FolderPath: p.Dir}}
				case "terminal list":
					result["terminals"] = []orca.Terminal{}
				case "folder-workspace rm":
					if flagValue(args, "--workspace") != "original" {
						t.Fatal("removed a foreign registration")
					}
					originalPresent = false
					removed++
				default:
					t.Fatalf("unexpected provider operation: %v", args)
				}
				return json.Marshal(map[string]any{"ok": true, "result": result})
			}}
			w.layout = orcaLayout{w: w, orca: c}
			if err := w.Cleanup(ctx, protocol.WorkspaceCleanup{WorkspaceID: as.Workspace.ID, Kind: "directory", Path: p.Dir, ProviderID: st.ProviderID}); err == nil {
				t.Fatal("cleanup accepted replacement provider identity")
			}
			if _, err := os.Stat(p.Dir); err != nil {
				t.Fatal("replacement's physical directory deleted")
			}
			if !replaceAfterRemoval && removed != 0 {
				t.Fatal("mutated provider after ownership conflict")
			}
		})
	}
}

func flagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
