package codexenv

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CacheInventory separates physical downloads from the declared loading policy.
// Project/admin configuration and connector authorization remain native facts;
// a directory on disk does not establish that its tools are callable.
type CacheInventory struct {
	ProcessCaches           int `json:"process_caches"`
	DownloadedVersions      int `json:"downloaded_versions"`
	SelectedVersions        int `json:"selected_versions"`
	OutsideSnapshotVersions int `json:"outside_snapshot_versions"`
}

func pluginCacheInventory(home string) (CacheInventory, error) {
	var out CacheInventory
	workspaces, err := os.ReadDir(filepath.Join(home, ".tend-processes"))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	for _, work := range workspaces {
		if !work.IsDir() {
			continue
		}
		root := filepath.Join(home, ".tend-processes", work.Name())
		raw, err := os.ReadFile(filepath.Join(root, "provenance.json"))
		if os.IsNotExist(err) {
			continue // A launch is preparing or its process just exited.
		}
		if err != nil {
			return out, err
		}
		var packages []Package
		if err = json.Unmarshal(raw, &packages); err != nil {
			return out, err
		}
		selected := map[string]bool{}
		for _, pkg := range packages {
			if pkg.Kind == "plugin" {
				selected[pkg.LocalID+":"+pkg.Version] = true
			}
		}
		out.ProcessCaches++
		cacheRoot := filepath.Join(root, "plugins", "cache")
		err = filepath.WalkDir(cacheRoot, func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if !entry.IsDir() || path == cacheRoot {
				return nil
			}
			rel, err := filepath.Rel(cacheRoot, path)
			if err != nil {
				return err
			}
			if len(strings.Split(rel, string(filepath.Separator))) != 3 {
				return nil
			}
			out.DownloadedVersions++
			name, market := filepath.Base(filepath.Dir(path)), filepath.Base(filepath.Dir(filepath.Dir(path)))
			if selected[name+"@"+market+":"+filepath.Base(path)] {
				out.SelectedVersions++
			} else {
				out.OutsideSnapshotVersions++
			}
			return filepath.SkipDir // Never traverse package scripts or assets.
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
