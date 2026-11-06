package journal

import (
	"fmt"
	"os"
)

// acquireWriter locks the actual database inode, so path aliases do not create
// another writer. The lock is advisory: replacement still has to prove that a
// predecessor without this lock has exited. The descriptor is never inherited
// by native worker processes and remains open until all journal writes stop.
func acquireWriter(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("journal: open writer ownership: %w", err)
	}
	fail := func(err error) (*os.File, error) {
		_ = file.Close()
		return nil, fmt.Errorf("journal: writer ownership: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() {
		return fail(fmt.Errorf("database is not a regular file"))
	}
	if err := lockWriter(file); err != nil {
		return fail(err)
	}
	again, err := os.Stat(path)
	if err != nil {
		return fail(err)
	}
	if !os.SameFile(info, again) {
		return fail(fmt.Errorf("database identity changed"))
	}
	return file, nil
}
