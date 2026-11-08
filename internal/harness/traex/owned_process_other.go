//go:build !linux

package traex

import (
	"context"
	"errors"
)

type ownedProcessIdentity struct{}

func ownedProcessSupported() bool { return false }
func inspectOwnedProcess(int) (ownedProcessIdentity, error) {
	return ownedProcessIdentity{}, errors.New("traex: independent native process recovery is unavailable on this platform")
}
func stopOwnedProcess(context.Context, ownedProcessIdentity, func() error) error {
	return errors.New("traex: independent native process recovery is unavailable on this platform")
}
func ownedDetachedDescendants(ownedProcessIdentity) ([]ownedProcessIdentity, error) {
	return nil, errors.New("traex: independent native process recovery is unavailable on this platform")
}
func confirmDetachedProcessesStopped([]ownedProcessIdentity) error {
	return errors.New("traex: independent native process recovery is unavailable on this platform")
}
func confirmOwnedProcessStopped(ownedProcessIdentity) error {
	return errors.New("traex: independent native process recovery is unavailable on this platform")
}
func awaitOwnedProcessStopped(context.Context, ownedProcessIdentity) error {
	return errors.New("traex: independent native process recovery is unavailable on this platform")
}
