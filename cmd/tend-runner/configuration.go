package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/agentregistry"
)

// checkConfiguration permits delivery-provider settings and additive agent
// installations. Identity, storage, workspaces and agent source stay pinned.
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
	if before.Agents != nil && after.Agents != nil {
		fixed := *after.Agents
		fixed.Installations = before.Agents.Installations
		if reflect.DeepEqual(*before.Agents, fixed) {
			if !reflect.DeepEqual(before.Agents.Installations, after.Agents.Installations) {
				if err := checkAgentInstallationAdditions(after.HostID, before.Agents, after.Agents); err != nil {
					return err
				}
			}
			after.Agents = before.Agents
		}
	}
	if !reflect.DeepEqual(before, after) {
		return errors.New("configuration maintenance may only change delivery providers or add agent installations")
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

func checkAgentInstallationAdditions(host string, before, after *agentcontrol.Config) error {
	previous := make(map[string]agentcontrol.Installation, len(before.Installations))
	for _, pin := range before.Installations {
		previous[pin.RuntimeID] = pin
	}
	installs := make([]agentregistry.InstallationReference, 0, len(after.Installations))
	for _, pin := range after.Installations {
		if old, ok := previous[pin.RuntimeID]; ok {
			if old != pin {
				return errors.New("configuration maintenance must preserve existing agent installations")
			}
			delete(previous, pin.RuntimeID)
		} else if err := checkAgentInstallationFile(pin); err != nil {
			return err
		}
		installs = append(installs, agentregistry.InstallationReference{RuntimeID: pin.RuntimeID, ArtifactID: pin.ArtifactID, ExpectedSHA256: pin.ExpectedSHA256, AdapterID: pin.AdapterID, AdapterRevision: pin.AdapterRevision, LocalReference: pin.LocalReference})
	}
	if len(previous) != 0 {
		return errors.New("configuration maintenance must retain existing agent installations")
	}
	if !filepath.IsAbs(after.SourceFile) {
		return errors.New("configuration maintenance requires a fixed absolute agent source")
	}
	raw, err := os.ReadFile(after.SourceFile)
	if err != nil {
		return err
	}
	cfg, err := agentconfig.Decode(raw)
	if err != nil {
		return err
	}
	for _, runtime := range cfg.Runtimes {
		if runtime.Kind == agentconfig.HostedManager {
			return errors.New("hosted definitions are not runner configurations")
		}
	}
	_, err = agentregistry.Prepare(host, cfg, agentconfig.SourceReference{Kind: agentconfig.LocalFile, Reference: after.SourceFile}, installs)
	return err
}

func checkAgentInstallationFile(pin agentcontrol.Installation) error {
	if !filepath.IsAbs(pin.LocalReference) {
		return errors.New("configuration maintenance requires an absolute agent installation")
	}
	f, err := os.Open(pin.LocalReference)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("agent installation must be a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != pin.ExpectedSHA256 {
		return errors.New("agent installation digest differs")
	}
	return nil
}
