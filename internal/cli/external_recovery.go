package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func runExternalRecovery(ctx context.Context, e *Env, args []string) error {
	flags := newFlags(e, "upgrade recover")
	bundle := flags.String("release", "", "complete local release; its new installer performs recovery")
	config := flags.String("config", "", "fixed local updater configuration")
	unit := flags.String("unit", "", "systemd user service; default is the unit using this configuration")
	id := flags.String("request-id", "", "original recovery identity; defaults to the fixed configuration and release")
	force := flags.Bool("force", false, "stop the exact local service and replace an unavailable updater")
	status := flags.Bool("status", false, "read the original recovery record without contacting the old service")
	dry := flags.Bool("dry-run", false, "print the new installer invocation")
	positional, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 || *bundle == "" || *config == "" || !*force && !*status && !*dry {
		return usagef("upgrade recover requires --release, --config and --force (or --status)")
	}
	directory, err := filepath.Abs(*bundle)
	if err != nil {
		return err
	}
	configuration, err := filepath.Abs(*config)
	if err != nil {
		return err
	}
	// The installer verifies the complete manifest before stopping anything.
	// The CLI pins the selected executable bytes using the same launcher as
	// other local installer operations, without importing server code.
	raw, err := os.ReadFile(filepath.Join(directory, "release-set.json"))
	if err != nil {
		return err
	}
	var set struct {
		Format    int                                             `json:"format"`
		Artifacts []struct{ Role, OS, Arch, Path, SHA256 string } `json:"artifacts"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return err
	}
	if set.Format < 2 {
		return usagef("external recovery requires a complete format 2 release")
	}
	installer, sum := "", ""
	for _, artifact := range set.Artifacts {
		if artifact.Role != "updater" || artifact.OS != runtime.GOOS || artifact.Arch != runtime.GOARCH {
			continue
		}
		if installer != "" || artifact.Path == "" || filepath.IsAbs(artifact.Path) || filepath.Clean(artifact.Path) != artifact.Path || artifact.Path == ".." || strings.HasPrefix(artifact.Path, ".."+string(filepath.Separator)) {
			return usagef("invalid installer selection")
		}
		installer, sum = filepath.Join(directory, artifact.Path), artifact.SHA256
	}
	if installer == "" {
		return usagef("release has no installer for this platform")
	}
	arguments := []string{"external-recover", "--release", directory, "--config", configuration}
	if *unit != "" {
		arguments = append(arguments, "--unit", *unit)
	}
	if *id != "" {
		arguments = append(arguments, "--request-id", *id)
	}
	next := append([]string{"tend", "upgrade", "recover"}, arguments[1:]...)
	next = append(next, "--status")
	if *status {
		arguments = append(arguments, "--status")
	} else if *force {
		arguments = append(arguments, "--force")
	}
	if *dry {
		return json.NewEncoder(e.Stdout).Encode(map[string]any{"installer": installer, "sha256": sum, "arguments": arguments})
	}
	return runPinnedBoundedInstaller(ctx, e, boundedInstallerInvocation{Installer: installer, InstallerSHA256: sum, Arguments: arguments, Action: "external-recover", Mutating: !*status, RequestID: *id, Timeout: 4 * time.Minute, NextCommand: next})
}
