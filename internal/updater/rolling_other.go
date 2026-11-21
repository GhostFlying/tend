//go:build !linux

package updater

import "context"

func (journalAuthority) AuthorizeLaunch(context.Context, Version, Binding, RollingLaunch, string) (Grant, error) {
	return Grant{}, ErrUnsupported
}
func RunnerHostToken(Config) (string, error) { return "", ErrUnsupported }
