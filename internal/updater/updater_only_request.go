package updater

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
)

// The independent installer owns these request IDs. Other local maintenance
// actions must not overwrite their identity or hide their reconciliation record.
func rejectUpdaterOnlyRequest(cfg Config, id string) error {
	if !protocol.ValidID(protocol.PrefixRequest, id) {
		return nil // The calling operation performs its normal input validation.
	}
	_, err := privateFile(filepath.Join(cfg.StateDir, "updater-only-handoffs", id+".json"))
	if err == nil {
		return errors.New("updater: request belongs to an independent updater-only replacement")
	}
	if !os.IsNotExist(err) {
		return err
	}
	raw, err := privateFile(filepath.Join(cfg.StateDir, "bounded-installers", id+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Only classify the existing installer action. Its complete window remains
	// owned and validated by the independent installer, without rewriting it.
	var installer struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &installer); err != nil {
		return err
	}
	if installer.Action == "updater_only_takeover" {
		return errors.New("updater: request belongs to an independent updater-only installer window")
	}
	return nil
}
