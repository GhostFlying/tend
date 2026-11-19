//go:build linux

package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Use the selected runner's transport even when the retained process predates
// rolling RPCs. This invocation never opens a journal or starts workers.
func (a journalAuthority) AuthorizeLaunch(ctx context.Context, v Version, b Binding, launch RollingLaunch) (Grant, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var grant Grant
	exe, err := os.Open(v.Binary)
	if err != nil {
		return grant, err
	}
	defer exe.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, launchContextReader{ctx, exe}); err != nil {
		return grant, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != b.ArtifactSHA256 {
		return grant, errors.New("updater: rolling grant helper binary differs from selected runner")
	}
	env, err := readEnvFiles(a.cfg.EnvFiles)
	if err != nil {
		return grant, err
	}
	input, err := json.Marshal(protocol.RollingAuthorization{Revision: launch.Revision, Request: protocol.RollingRunnerGrantRequest{Binding: b, Updater: launch.Observation}})
	if err != nil {
		return grant, err
	}
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", append([]string{"rolling-authorize"}, a.cfg.Args...)...)
	cmd.ExtraFiles = []*os.File{exe}
	cmd.Dir = a.cfg.Dir
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(input)
	cmd.WaitDelay = time.Second
	output, err := cmd.Output()
	if err != nil {
		return grant, errors.New("updater: runner could not confirm rolling launch with server")
	}
	err = json.Unmarshal(output, &grant)
	return grant, err
}
