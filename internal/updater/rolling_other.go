//go:build !linux

package updater

import "context"

func (journalAuthority) AuthorizeLaunch(context.Context, Version, Binding, RollingLaunch) (Grant, error) {
	return Grant{}, ErrUnsupported
}
