package rollout

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRolloutDirectoryAliasesPreserveReadAndArtifactBoundaries(t *testing.T) {
	physical, aliasParent := t.TempDir(), t.TempDir()
	alias := filepath.Join(aliasParent, "native-home")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skip(err)
	}
	root := filepath.Join(physical, "sessions")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "rollout-session.jsonl")
	artifact := filepath.Join(root, "rollout-session.artifacts")
	if err := os.Mkdir(artifact, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(artifact, "output.txt"), "native tool output")
	write(t, path, header()+start("turn")+item("done"))
	for _, tc := range []struct{ name, configured, native string }{
		{"canonical_native", filepath.Join(alias, "sessions"), path},
		{"alias_native", root, filepath.Join(alias, "sessions", filepath.Base(path))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Open([]string{tc.configured}, tc.native)
			if err != nil {
				t.Fatal("same native directory was rejected", err)
			}
			body, err := io.ReadAll(f)
			_ = f.Close()
			if err != nil || string(body) != header()+start("turn")+item("done") {
				t.Fatal("native original changed", err)
			}
			r := New("epoch", tc.configured)
			read(t, r, protocol.RolloutPreviewSource{SessionID: "session", TurnID: "turn", Path: tc.native}, protocol.RolloutPreviewQuery{})
			f, err = r.openReference(tc.native, "output.txt")
			if err != nil {
				t.Fatal("native artifact was rejected", err)
			}
			_ = f.Close()
			outside := filepath.Join(t.TempDir(), "private.jsonl")
			write(t, outside, "private")
			if f, err = Open([]string{tc.configured}, outside); err == nil {
				_ = f.Close()
				t.Fatal("directory alias authorized an unrelated original")
			}
			missing := filepath.Join(filepath.Dir(tc.native), "missing.jsonl")
			if _, err = Open([]string{tc.configured}, missing); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("missing original lost its error", err)
			}
			link := filepath.Join(filepath.Dir(tc.native), "outside.jsonl")
			if err = os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(link)
			if f, err = Open([]string{tc.configured}, link); err == nil {
				_ = f.Close()
				t.Fatal("native symlink escaped its configured root")
			}
		})
	}
	if err := os.Remove(filepath.Join(artifact, "output.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(artifact); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), artifact); err != nil {
		t.Fatal(err)
	}
	if f, err := New("epoch", filepath.Join(alias, "sessions")).openReference(path, "output.txt"); err == nil {
		_ = f.Close()
		t.Fatal("artifact directory symlink was accepted")
	}
}
