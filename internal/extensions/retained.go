package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/protocol"
)

// RetainedBinding restores an already validated local snapshot without checking
// commands, credentials or the current base config needed for new work. The
// run's persisted BindExecution record remains authoritative at native startup.
func (c *Consumer) RetainedBinding(ctx context.Context, pin protocol.ExtensionExecutionPin, runtime string) (*codexenv.Binding, error) {
	return c.RetainedScopedBinding(ctx, pin, runtime, "")
}

func (c *Consumer) RetainedScopedBinding(ctx context.Context, pin protocol.ExtensionExecutionPin, runtime, scope string) (*codexenv.Binding, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	release, err := c.Release(pin.ReleaseID)
	if err != nil {
		return nil, err
	}
	if _, err = VerifyCombination(release, pin); err != nil {
		return nil, err
	}
	category := pin.CategoryID
	if scope != "" {
		category = ""
	}
	key, _ := json.Marshal(struct {
		Scope    string
		Category string
		Agent    protocol.AgentIdentity
		Runtime  string
	}{scope, category, pin.Agent, runtime})
	root := filepath.Join(c.Config.Directory, "views", Hash(key), "inputs")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), pin.EffectiveSHA256+"-") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name(), "prepared.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var binding codexenv.Binding
		if json.Unmarshal(raw, &binding) != nil || binding.Extensions != pin || !filepath.IsAbs(binding.Directory) || !ValidHash(binding.Generation) {
			continue
		}
		status, err := (&codexenv.Service{Root: binding.Directory}).Read(binding.ID)
		if err != nil {
			return nil, err
		}
		if status.Profile.ManagedRuntimeID == runtime && slices.ContainsFunc(status.Snapshots, func(s codexenv.Snapshot) bool { return s.ID == binding.Generation }) {
			return &binding, nil
		}
	}
	return nil, fmt.Errorf("original managed extension snapshot is not prepared locally")
}
