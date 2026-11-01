package gitws

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
)

func (w *Workspaces) inheritedChanges(ctx context.Context, as protocol.Assignment, home string, st state) error {
	var spec struct {
		Workspace struct {
			SourceTaskID string `json:"source_task_id"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(as.Spec, &spec); err != nil {
		return err
	}
	f := protocol.RunPreparationFailure{Phase: protocol.PreparationPhaseWorkspace, Code: protocol.PreparationInheritedChanges,
		WorkspaceID: as.Workspace.ID, SourceTaskID: spec.Workspace.SourceTaskID, RequiredField: protocol.PreparationIncludeField,
		Changes: []protocol.PreparationChange{}}
	for _, m := range as.Workspace.Mounts {
		dir := st.Mounts[m.Path].dir(home, m.Path)
		// Preserve leading status spaces and NUL-delimited paths. git.run trims
		// whitespace and must not be used to parse porcelain records.
		raw, err := w.git.with("GIT_OPTIONAL_LOCKS=0").cmd(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
		if err != nil {
			return err
		}
		head, err := w.git.run(ctx, dir, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		records := strings.Split(string(raw), "\x00")
		for i := 0; i < len(records); i++ {
			record := records[i]
			if len(record) < 4 {
				continue
			}
			status, path := record[:2], record[3:]
			if strings.ContainsAny(status, "RC") {
				i++
			}
			f.ChangeCount++
			if len(f.Changes) == protocol.MaxPreparationChanges || len(path) > 512 || !utf8.ValidString(path) || strings.ContainsFunc(path, unicode.IsControl) {
				f.Truncated = true
				continue
			}
			f.Changes = append(f.Changes, protocol.PreparationChange{Mount: m.Path, Path: path, Status: status, HEAD: head})
		}
	}
	return &runner.PreparationError{Failure: f}
}
