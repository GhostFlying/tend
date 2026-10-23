//go:build !linux

package updater

func BootClock() (string, int64, error) { return "", 0, ErrUnsupported }
