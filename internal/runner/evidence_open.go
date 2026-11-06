package runner

import (
	"context"
	"errors"
	"os"
)

var errEvidenceNonRegular = errors.New("evidence source must be a regular file")

// openRegularEvidence rejects special files before opening and checks the
// opened object again, since a selected path may change between those steps.
func openRegularEvidence(ctx context.Context, path string) (*os.File, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, nil, errEvidenceNonRegular
	}
	f, err := openEvidenceFile(path)
	if err != nil {
		return nil, nil, err
	}
	st, err = f.Stat()
	if err == nil && !st.Mode().IsRegular() {
		err = errEvidenceNonRegular
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, st, nil
}
