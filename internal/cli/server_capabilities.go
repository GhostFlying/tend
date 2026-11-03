package cli

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
	"path/filepath"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runServerCapabilities(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "upgrade capabilities")
	binary := fs.String("binary", "", "explicit server artifact to inspect without configuration or network")
	expected := fs.String("sha256", "", "expected immutable artifact SHA256")
	fs.Bool("json", true, "print the typed executable capability report")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	digest, err := hex.DecodeString(*expected)
	if err != nil || len(digest) != sha256.Size || len(rest) != 0 || !filepath.IsAbs(*binary) {
		return usagef("upgrade capabilities requires --binary absolute-path and --sha256")
	}
	file, err := os.Open(*binary)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<30 {
		return usagef("server artifact must be a bounded regular file")
	}
	directory, err := os.MkdirTemp("", "tend-server-capabilities-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	pinned := filepath.Join(directory, "server")
	target, err := os.OpenFile(pinned, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	sum := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(sum, target), io.LimitReader(file, (1<<30)+1))
	closeErr := target.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > 1<<30 || hex.EncodeToString(sum.Sum(nil)) != *expected {
		return usagef("server artifact SHA256 differs from the selected identity")
	}
	if err := os.Chmod(pinned, 0o700); err != nil {
		return err
	}
	childCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(childCtx, pinned, "--capabilities")
	command.Env = []string{}
	var output serverCapabilityOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return errors.New("server capability inspection failed; no ordinary startup was attempted")
	}
	var report protocol.ServerCapabilities
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return usagef("invalid typed server capability response")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return usagef("trailing server capability response")
	}
	if err := report.Validate(*expected); err != nil {
		return err
	}
	return (owner{e: e, json: true}).printJSON(report)
}

type serverCapabilityOutput struct{ bytes.Buffer }

func (w *serverCapabilityOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 64<<10 {
		return 0, errors.New("server capability output exceeds limit")
	}
	return w.Buffer.Write(p)
}
