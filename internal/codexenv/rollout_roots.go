package codexenv

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
)

// RecordedSessionRoots reads the small append-only home registry, independently
// of current runtime mappings and without scanning historical run directories.
func RecordedSessionRoots(directory string) ([]string, error) {
	if directory == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(directory, "homes.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var roots []string
	if err = json.Unmarshal(raw, &roots); err != nil {
		return nil, err
	}
	return roots, nil
}

// RecordSessionHome preserves transcript access for mapped executions after a
// mapping is replaced or removed. It records only fixed native home roots.
func RecordSessionHome(ctx context.Context, directory string, binding *Binding) error {
	if directory == "" || binding == nil {
		return nil
	}
	status, err := (&Service{Root: binding.Directory}).Read(binding.ID)
	if err != nil {
		return err
	}
	root := filepath.Join(status.Profile.Home, "sessions")
	release, err := lock(ctx, directory)
	if err != nil {
		return err
	}
	defer release()
	roots, err := RecordedSessionRoots(directory)
	if err != nil {
		return err
	}
	if slices.Contains(roots, root) {
		return nil
	}
	roots = append(roots, root)
	slices.Sort(roots)
	return writeJSON(filepath.Join(directory, "homes.json"), roots)
}
