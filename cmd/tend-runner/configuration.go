package main

import (
	"errors"
	"path/filepath"
	"reflect"

	"github.com/GhostFlying/tend/internal/cfgfile"
)

// checkConfiguration intentionally permits only delivery-provider settings.
// Transport, identity, storage, slots, workspaces and credentials stay pinned.
func checkConfiguration(previous, candidate string) error {
	var before, after config
	if err := cfgfile.Load(previous, &before); err != nil {
		return err
	}
	if err := cfgfile.Load(candidate, &after); err != nil {
		return err
	}
	observers := after.DeliveryObserver
	after.DeliveryObserver = before.DeliveryObserver
	if !reflect.DeepEqual(before, after) {
		return errors.New("configuration maintenance may only change delivery_observer")
	}
	for _, p := range observers.Providers {
		path := observers.GitHubBin
		if p == "codebase" {
			path = observers.CodebaseBin
		}
		if !filepath.IsAbs(path) {
			return errors.New("configuration maintenance requires explicit provider executable paths")
		}
	}
	_, _, err := observers.build()
	return err
}
