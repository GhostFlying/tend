package updater

import (
	"errors"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
)

// boundedServerDataIdentity reads the ledger selected by the fixed local config.
// Its schema version is provenance; migrations must preserve its file identity.
func boundedServerDataIdentity(cfg Config) (*protocol.BoundedDataIdentity, error) {
	if cfg.Role != "server" || len(cfg.DataFiles) != 1 || !filepath.IsAbs(cfg.DataFiles[0]) {
		return nil, errors.New("updater: bounded server requires one fixed ledger file")
	}
	path := filepath.Clean(cfg.DataFiles[0])
	device, inode, err := BoundedDataFileIdentity(path)
	if err != nil {
		return nil, err
	}
	return &protocol.BoundedDataIdentity{Path: path, Device: device, Inode: inode, Version: cfg.StorageVersion}, nil
}

func validateBoundedServerData(cfg Config, data *protocol.BoundedDataIdentity) error {
	actual, err := boundedServerDataIdentity(cfg)
	if err != nil {
		return err
	}
	if data == nil || data.Path != actual.Path || data.Device != actual.Device || data.Inode != actual.Inode || data.Version <= 0 {
		return errors.New("updater: bounded server ledger identity changed")
	}
	return nil
}
