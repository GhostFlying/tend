//go:build !linux

package updater

import "os"

func syncDir(string) error             { return ErrUnsupported }
func lockDir(string) (*os.File, error) { return nil, ErrUnsupported }
func unlock(f *os.File) error {
	if f != nil {
		return f.Close()
	}
	return nil
}
func supported() error { return ErrUnsupported }
