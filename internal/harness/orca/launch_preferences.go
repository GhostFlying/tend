package orca

import (
	"context"
	"errors"
	"slices"
)

const traeModelLaunchCapability = "agent.launch.trae-model.v1"

var ErrTraeModelSelectionUnsupported = errors.New("orca CLI or host does not support Trae model selection")

// CheckTraeModelSelection requires both the local CLI parser and execution host.
func (c *Client) CheckTraeModelSelection(ctx context.Context) error {
	var status struct {
		Client struct {
			Capabilities []string `json:"capabilities"`
		} `json:"client"`
		Runtime struct {
			RuntimeID    string   `json:"runtimeId"`
			Capabilities []string `json:"capabilities"`
		} `json:"runtime"`
	}
	if err := c.call(ctx, "", &status, "status"); err != nil {
		return err
	}
	if status.Runtime.RuntimeID == "" {
		return errors.New("orca execution host is unavailable")
	}
	if !slices.Contains(status.Client.Capabilities, traeModelLaunchCapability) || !slices.Contains(status.Runtime.Capabilities, traeModelLaunchCapability) {
		return ErrTraeModelSelectionUnsupported
	}
	return nil
}
