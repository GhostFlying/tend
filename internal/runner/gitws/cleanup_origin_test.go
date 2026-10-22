package gitws

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestDirectoryCleanupChecksCurrentProviderAcrossOriginChanges(t *testing.T) {
	for _, original := range []string{"", "folder:original"} {
		for _, adapter := range []bool{false, true} {
			for _, registration := range []string{"missing", "matching", "foreign", "live"} {
				name := "plain-origin"
				if original != "" {
					name = "orca-origin"
				}
				if adapter {
					name += "/orca-adapter"
				} else {
					name += "/plain-adapter"
				}
				t.Run(name+"/"+registration, func(t *testing.T) {
					w := New(Config{Home: t.TempDir()})
					as := protocol.Assignment{TaskID: "task", Role: protocol.RoleImplement, Workspace: protocol.AssignmentWorkspace{ID: "ws_cleanup", Kind: "directory"}}
					p, err := w.Prepare(t.Context(), as)
					if err != nil {
						t.Fatal(err)
					}
					marker := filepath.Join(p.Dir, "owner-work.txt")
					if err := os.WriteFile(marker, []byte("preserve unless authorized"), 0o600); err != nil {
						t.Fatal(err)
					}
					st, err := loadState(p.Dir)
					if err != nil {
						t.Fatal(err)
					}
					st.ProviderID = original
					if err := saveState(p.Dir, st); err != nil {
						t.Fatal(err)
					}
					removed, listed := 0, 0
					registered := registration != "missing"
					if adapter {
						c := &orca.Client{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
							result := map[string]any{}
							switch args[0] + " " + args[1] {
							case "folder-workspace list":
								listed++
								folders := []orca.FolderWorkspace{}
								if registered {
									id := "original"
									if registration == "foreign" {
										id = "foreign"
									}
									folders = append(folders, orca.FolderWorkspace{ID: id, FolderPath: p.Dir})
								}
								result["folderWorkspaces"] = folders
							case "terminal list":
								terminals := []orca.Terminal{}
								if registration == "live" {
									terminals = append(terminals, orca.Terminal{Handle: "human-terminal"})
								}
								result["terminals"] = terminals
							case "folder-workspace rm":
								if original == "" || flagValue(args, "--workspace") != "original" {
									t.Fatal("removed unowned provider registration")
								}
								removed++
								registered = false
							default:
								t.Fatalf("unexpected command: %v", args)
							}
							return json.Marshal(map[string]any{"ok": true, "result": result})
						}}
						w.layout = orcaLayout{w: w, orca: c}
					}
					err = w.Cleanup(t.Context(), protocol.WorkspaceCleanup{WorkspaceID: as.Workspace.ID, Kind: "directory", Path: p.Dir, ProviderID: original})
					blocked := original != "" && !adapter
					if adapter && registration != "missing" {
						blocked = original == "" || registration == "foreign" || registration == "live"
					}
					if blocked {
						if err == nil {
							t.Fatal("cleanup bypassed current provider ownership or live terminal")
						}
						if b, readErr := os.ReadFile(marker); readErr != nil || string(b) != "preserve unless authorized" {
							t.Fatalf("owner files changed on rejected cleanup: %q %v", b, readErr)
						}
						if removed != 0 {
							t.Fatal("provider mutated during rejected cleanup")
						}
					} else {
						if err != nil {
							t.Fatal(err)
						}
						if _, err := os.Stat(p.Dir); !os.IsNotExist(err) {
							t.Fatalf("authorized owned directory remained: %v", err)
						}
					}
					if adapter && listed == 0 {
						t.Fatal("current adapter path registrations were not consulted")
					}
				})
			}
		}
	}
}
