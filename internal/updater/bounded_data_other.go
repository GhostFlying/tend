//go:build !linux

package updater

func BoundedDataFileIdentity(string) (uint64, uint64, error) { return 0, 0, ErrUnsupported }
