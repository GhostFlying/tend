package updater

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

func privateFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("updater: private file path must be absolute")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	lst, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || lst.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0o077 != 0 || !ownedFile(st) || !os.SameFile(st, lst) || st.Size() > 4<<20 {
		return nil, errors.New("updater: expected bounded private operator-owned regular file")
	}
	return io.ReadAll(io.LimitReader(f, 4<<20))
}

// ReadHandoffPlan rejects shared or replaceable authorization documents before
// the installer can perform any manager action.
func ReadHandoffPlan(path string) (HandoffPlan, error) {
	var p HandoffPlan
	raw, err := privateFile(path)
	if err != nil {
		return p, err
	}
	err = strictJSON(raw, &p)
	return p, err
}
