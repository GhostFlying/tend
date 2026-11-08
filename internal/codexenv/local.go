package codexenv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/GhostFlying/tend/internal/procx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/tailscale/hujson"
)

func (s *Service) Local(ctx context.Context, q protocol.LocalCodexEnvironmentRequest) (json.RawMessage, error) {
	if q.Action == "watch run" {
		return nil, s.Watch(ctx)
	}
	if q.Action == "watch status" {
		status, err := s.Read("watch-control")
		if err == ErrNotFound {
			return json.Marshal(map[string]bool{"enabled": false})
		}
		if err != nil {
			return nil, err
		}
		name := watchUnitName(s.Root)
		cmd := exec.CommandContext(ctx, "systemctl", "--user", "is-active", name)
		procx.Contain(cmd)
		out, callErr := cmd.Output()
		procx.KillGroup(cmd)
		runtimeState := strings.TrimSpace(string(out))
		if callErr != nil && runtimeState == "" {
			runtimeState = "unavailable"
		}
		return json.Marshal(map[string]any{"enabled": status.WatchEnabled, "revision": status.Revision, "runtime_state": runtimeState})
	}
	request := Request{Action: q.Action, ID: q.ID, RequestID: q.RequestID, ExpectedRevision: q.ExpectedRevision, Item: Item{ID: q.ItemID, Path: q.Source}, Generation: q.Generation, DryRun: q.DryRun}
	request.After, request.Limit = q.After, q.Limit
	if strings.HasPrefix(q.Action, "watch ") {
		request.ID = "watch-control"
	}
	if len(q.Data) > 0 {
		raw, err := hujson.Standardize([]byte(q.Data))
		if err != nil {
			return nil, fmt.Errorf("invalid environment HuJSON")
		}
		var p Profile
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("invalid environment description: %w", err)
		}
		if p.UserHome == "" {
			p.UserHome = s.UserHome
		}
		if p.SourceHome == "" && p.AuthSource == "" {
			p.SourceHome = s.UserHome + string(os.PathSeparator) + ".codex"
		}
		request.Profile = &p
	}
	result, err := s.Execute(ctx, request)
	raw, encodeErr := json.Marshal(result)
	if encodeErr != nil {
		return nil, encodeErr
	}
	return raw, err
}
