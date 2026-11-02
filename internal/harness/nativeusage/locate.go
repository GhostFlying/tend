package nativeusage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Locate considers only filenames mentioning an already registered thread.
// Discovery subsequently verifies metadata before reading any measurements.
func Locate(ctx context.Context, home, provider, thread string) (Source, error) {
	var found Source
	stop := errors.New("located")
	for _, metadataFallback := range []bool{false, true} {
		for _, name := range []string{"sessions", "archived_sessions", "cli/sessions", "cli/archived_sessions"} {
			root := filepath.Join(home, name)
			err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						return nil
					}
					return err
				}
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || (!metadataFallback && !strings.Contains(e.Name(), thread)) {
					return nil
				}
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				m, _, err := header(f)
				_ = f.Close()
				if err != nil || (m.ID != thread && m.SessionID != thread) {
					return nil
				}
				found = Source{Path: path, Provider: provider, ThreadID: thread, SessionID: m.SessionID}
				return stop
			})
			if errors.Is(err, stop) {
				return found, nil
			}
			if err != nil {
				return found, err
			}
		}
	}
	return found, errors.New("native usage: registered thread history is unavailable")
}
