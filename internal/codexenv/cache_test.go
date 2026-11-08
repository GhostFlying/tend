package codexenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPluginCacheDistinguishesDownloadsFromSnapshotSelection(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state [view]")
	work := filepath.Join(home, ".tend-processes", "launch-fixture")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "provenance.json"), []byte(`[{"kind":"plugin","local_id":"selected@private","version":"1.0"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"private/selected/1.0/assets/empty", "account/other/2.0/skills"} {
		if err := os.MkdirAll(filepath.Join(work, "plugins", "cache", filepath.FromSlash(path)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := pluginCacheInventory(home)
	if err != nil || got.ProcessCaches != 1 || got.DownloadedVersions != 2 || got.SelectedVersions != 1 || got.OutsideSnapshotVersions != 1 {
		t.Fatal("downloaded cache was confused with the environment's loading selection", got, err)
	}
}
