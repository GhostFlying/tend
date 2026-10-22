package gitws

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
)

const maxWireBaseline = 512 << 10

func baselineReport(full json.RawMessage, kind, taskID string) json.RawMessage {
	if len(full) <= maxWireBaseline {
		return full
	}
	digest := sha256.Sum256(full)
	report := map[string]any{"kind": kind, "sha256": hex.EncodeToString(digest[:]), "bytes": len(full), "manifest_external": true, "local_snapshot_path": filepath.ToSlash(filepath.Join(".tend", "baselines", taskID+".json"))}
	if kind == "git" {
		var mounts []struct {
			Path            string               `json:"path"`
			Head            string               `json:"head"`
			TaskBase        string               `json:"task_base,omitempty"`
			Tree            string               `json:"tree"`
			Index           string               `json:"index_tree"`
			Entries         []baselineIndexEntry `json:"index_entries,omitempty"`
			ConflictedIndex bool                 `json:"conflicted_index,omitempty"`
			UnmergedEntries int                  `json:"unmerged_entries,omitempty"`
		}
		if json.Unmarshal(full, &mounts) == nil {
			for i, m := range mounts {
				for _, entry := range m.Entries {
					if entry.Stage != 0 {
						mounts[i].UnmergedEntries++
					}
				}
				mounts[i].ConflictedIndex = mounts[i].UnmergedEntries > 0
				mounts[i].Entries = nil
			}
			report["mounts"] = mounts
		}
	}
	out, _ := json.Marshal(report)
	return out
}
