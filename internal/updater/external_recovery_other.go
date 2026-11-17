//go:build !linux

package updater

import (
	"context"
	"os"
)

func ExternalRecovery(context.Context, Config, ExternalRecoveryRequest) (ExternalRecoveryRecord, error) {
	return ExternalRecoveryRecord{}, ErrUnsupported
}
func externalInstallerLock(Config) (*os.File, error) { return nil, ErrUnsupported }
