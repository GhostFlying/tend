package gitws

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/GhostFlying/tend/internal/procx"
)

// git runs the git CLI. Prompts are disabled so a missing credential fails
// instead of hanging an unattended runner.
type git struct {
	bin string
	env []string
}

func (g git) cmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, g.bin, append([]string{"-C", dir}, args...)...)
	c.Env = append(append(os.Environ(), g.env...), "GIT_TERMINAL_PROMPT=0")
	procx.Contain(c)
	return c
}

// userinfo matches the credentials part of a URL.
var userinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s]+@`)

// redact removes credentials from text that may quote remote URLs, so
// diagnostics can be logged.
func redact(s string) string { return userinfo.ReplaceAllString(s, "${1}***@") }

func describe(args []string, err error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	if len(msg) > 2000 {
		msg = msg[:2000] + "…"
	}
	return fmt.Errorf("git %s: %w: %s", redact(strings.Join(args, " ")), err, redact(msg))
}

// with returns g with env added to its environment.
func (g git) with(env ...string) git {
	g.env = append(append([]string(nil), g.env...), env...)
	return g
}

// run returns the trimmed standard output of a git command.
func (g git) run(ctx context.Context, dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	c := g.cmd(ctx, dir, args...)
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return "", describe(args, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// test runs a git command whose exit status 1 means false, such as
// diff --quiet or rev-parse --verify --quiet.
func (g git) test(ctx context.Context, dir string, args ...string) (bool, error) {
	var stderr bytes.Buffer
	c := g.cmd(ctx, dir, args...)
	c.Stderr = &stderr
	err := c.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.ExitCode() == 1 && ctx.Err() == nil {
		return false, nil
	}
	return false, describe(args, err, stderr.String())
}
