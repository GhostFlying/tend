package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func syncHostedConfiguration(ctx context.Context, a *app.App, ids protocol.IDGen, prepared *hostedconfig.Prepared) error {
	current, err := a.HostedAgentInventory(ctx)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	expected := current.ConfigurationRevision
	payload, err := json.Marshal(map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()})
	if err != nil {
		return err
	}
	env := &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "hosted.configure", Target: protocol.Target{Type: "hosted_configuration"}, ExpectedControlRevision: &expected, Payload: payload}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, app.AcceptHostedConfiguration{Prepared: prepared})
	if err != nil {
		return err
	}
	if op.Status != protocol.OpSucceeded {
		return fmt.Errorf("accept named hosted configuration: %w", op.Error)
	}
	return nil
}

// ExecutableRef is the sole named-mode launch source. Metadata locators and
// the legacy main-agent binary are not overrides for this binding.
func hostedExecutable(reference string) (string, error) {
	if strings.ContainsAny(reference, "/\\") || strings.HasPrefix(reference, "~") {
		return cfgfile.Path(reference)
	}
	return exec.LookPath(reference)
}
