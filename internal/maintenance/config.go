// Package maintenance supervises standalone, observable native maintenance sessions.
package maintenance

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
)

type Config struct {
	InstanceID       string                      `json:"instance_id"`
	UpdaterSocket    string                      `json:"updater_socket,omitempty"`
	StateDir         string                      `json:"state_dir"`
	ControlSocket    string                      `json:"control_socket"`
	Workspace        string                      `json:"workspace"`
	ServerURL        string                      `json:"server_url"`
	TokenFile        string                      `json:"token_file"`
	Backend          protocol.MaintenanceBackend `json:"backend"`
	InstructionsFile string                      `json:"instructions_file"`
	CheckSeconds     int                         `json:"check_seconds,omitempty"`
}

func ValidateBackend(b protocol.MaintenanceBackend) error {
	for _, root := range b.WritableRoots {
		if !filepath.IsAbs(root) {
			return errors.New("maintenance: writable_roots must be absolute")
		}
	}
	if (b.Agent != "codex" && b.Agent != "traex") || !filepath.IsAbs(b.Socket) {
		return errors.New("maintenance: require codex or traex and an absolute shared app-server socket")
	}
	if b.ApprovalPolicy != "never" && b.ApprovalPolicy != "on-request" {
		return errors.New("maintenance: approval_policy must be never or on-request")
	}
	if b.Sandbox != "read-only" && b.Sandbox != "workspace-write" && b.Sandbox != "danger-full-access" {
		return errors.New("maintenance: invalid native sandbox")
	}
	return nil
}

func ReadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	var c Config
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, errors.New("maintenance: trailing configuration")
	}
	for _, p := range []string{c.StateDir, c.ControlSocket, c.Workspace, c.TokenFile, c.InstructionsFile} {
		if !filepath.IsAbs(p) {
			return c, errors.New("maintenance: configured filesystem paths must be absolute")
		}
	}
	if c.CheckSeconds == 0 {
		c.CheckSeconds = 300
	}
	if c.CheckSeconds < 1 {
		return c, errors.New("maintenance: invalid check interval")
	}
	if c.InstanceID == "" || c.UpdaterSocket != "" && !filepath.IsAbs(c.UpdaterSocket) {
		return c, errors.New("maintenance: require instance_id and an absolute optional updater socket")
	}
	return c, ValidateBackend(c.Backend)
}
