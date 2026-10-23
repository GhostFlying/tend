package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runnerBuild(version string) (protocol.RunnerBuild, error) {
	executable, err := os.Executable()
	if err != nil {
		return protocol.RunnerBuild{}, err
	}
	f, err := os.Open(executable)
	if err != nil {
		return protocol.RunnerBuild{}, err
	}
	defer f.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, f); err != nil {
		return protocol.RunnerBuild{}, fmt.Errorf("hash executable: %w", err)
	}
	info := protocol.RunnerBuild{Version: version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, PID: int64(os.Getpid()), BinarySHA256: hex.EncodeToString(digest.Sum(nil))}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			if setting.Key == "vcs.revision" {
				info.Commit = setting.Value
			}
		}
	}
	return info, nil
}
