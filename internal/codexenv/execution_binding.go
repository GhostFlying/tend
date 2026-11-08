package codexenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

type executionBinding struct {
	Execution string   `json:"execution"`
	Binding   *Binding `json:"binding"`
}

// BindExecution freezes the local environment identity before native startup.
// A legacy thread without a record keeps its original unisolated home. Neither
// a changed runtime mapping nor its removal can redirect an existing execution.
func BindExecution(ctx context.Context, state, execution string, current *Binding, resumed bool) (*Binding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if state == "" {
		if current == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("environment execution needs a fixed state directory")
	}
	path := filepath.Join(state, "codex-environment.json")
	saved, err := readExecutionBinding(state, execution)
	if !errors.Is(err, os.ErrNotExist) {
		return saved, err
	}
	if resumed {
		current = nil
	}
	// Unconfigured executions retain the existing cross-platform behavior. Their
	// original native thread also prevents a later mapping from being adopted.
	if current == nil {
		return nil, nil
	}
	if execution == "" || !filepath.IsAbs(state) {
		return nil, fmt.Errorf("invalid environment execution state")
	}
	copy := *current
	copy.Directory = canonicalPath(copy.Directory)
	if _, err = (&Service{Root: copy.Directory}).directory(copy.ID); err != nil {
		return nil, err
	}
	release, err := lock(ctx, state)
	if err != nil {
		return nil, err
	}
	defer release()
	saved, err = readExecutionBinding(state, execution)
	if !errors.Is(err, os.ErrNotExist) {
		return saved, err
	}
	if err = writeJSON(path, executionBinding{Execution: execution, Binding: &copy}); err != nil {
		return nil, err
	}
	return &copy, nil
}

// SessionRoots returns fixed homes for the configured runtime mappings. Explicit
// rollout root settings remain the caller's authority.
func (b *Bindings) SessionRoots() ([]string, error) {
	if b == nil {
		return nil, nil
	}
	roots := []string{}
	var unavailable error
	seen := map[string]bool{}
	for _, id := range b.Runtimes {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		status, err := (&Service{Root: b.Directory}).Read(id)
		if err != nil {
			unavailable = errors.Join(unavailable, fmt.Errorf("environment %s: %w", id, err))
			continue
		}
		roots = append(roots, filepath.Join(status.Profile.Home, "sessions"))
	}
	slices.Sort(roots)
	return slices.Compact(roots), unavailable
}

func readExecutionBinding(state, execution string) (*Binding, error) {
	raw, err := os.ReadFile(filepath.Join(state, "codex-environment.json"))
	if err != nil {
		return nil, err
	}
	var saved executionBinding
	if err = json.Unmarshal(raw, &saved); err != nil {
		return nil, err
	}
	if saved.Execution != execution {
		return nil, fmt.Errorf("environment execution identity differs")
	}
	if saved.Binding != nil {
		if _, err = (&Service{Root: saved.Binding.Directory}).directory(saved.Binding.ID); err != nil {
			return nil, err
		}
	}
	return saved.Binding, nil

}

// InheritExecution binds a question continuation to its stopped predecessor's
// home, even though the continuation has a new run and snapshot reference.
func InheritExecution(ctx context.Context, state, execution, previousState, previousExecution string) (*Binding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if saved, err := readExecutionBinding(state, execution); !errors.Is(err, os.ErrNotExist) {
		return saved, err
	}
	info, err := os.Stat(previousState)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("original execution state is not a directory")
	}
	original, err := BindExecution(ctx, previousState, previousExecution, nil, true)
	if err != nil {
		return nil, err
	}
	return BindExecution(ctx, state, execution, original, false)
}

// ConfirmExecutionClosed records an independently verified native close. The
// caller must first prove the original process group and descendants stopped.
// It never selects a new binding or creates a missing adoption reference.
func ConfirmExecutionClosed(ctx context.Context, state, execution string) error {
	if state == "" {
		return nil
	}
	binding, err := readExecutionBinding(state, execution)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || binding == nil {
		return err
	}
	dir, err := (&Service{Root: binding.Directory}).directory(binding.ID)
	if err != nil {
		return err
	}
	release, err := lock(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	prior, ok, err := readAdoption(ctx, dir, execution)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if prior.State == "retained" {
		return nil
	}
	prior.State = "retained"
	return writeAdoption(ctx, dir, prior)
}
