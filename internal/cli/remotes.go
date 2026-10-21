package cli

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// remotesTimeout bounds the git call: the hook must answer in time.
const remotesTimeout = time.Second

// remotes returns the URLs of the git remotes of the repository at dir, or
// of the working directory when dir is empty, so tend can bind the
// session to that repository's project (D59). Outside a repository, or
// when git fails, it returns nothing. Only remote URLs leave the machine,
// never local paths.
func (e *Env) remotes(ctx context.Context, dir string) []string {
	if dir == "" && e.Getwd != nil {
		dir, _ = e.Getwd()
	}
	if dir == "" || e.Output == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, remotesTimeout)
	defer cancel()
	// git remote -v shows the URLs as git uses them, with insteadOf
	// rewrites applied: "<name>\t<url> (fetch)".
	out, err := e.Output(ctx, "git", "-C", dir, "remote", "-v")
	if err != nil {
		return nil
	}
	var urls []string
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		remote := withoutCredentials(fields[1])
		if networkRemote(remote) && len(remote) <= protocol.MaxRemoteLen && !slices.Contains(urls, remote) {
			urls = append(urls, remote)
		}
		if len(urls) == protocol.MaxRemotes {
			break
		}
	}
	return urls
}

// networkRemote reports whether a remote URL names a repository on another
// host rather than a local path, which must not leave the machine: a URL
// with a scheme other than file, or git's scp-like user@host:path.
func networkRemote(url string) bool {
	if scheme, _, ok := strings.Cut(url, "://"); ok {
		return scheme != "" && !strings.EqualFold(scheme, "file")
	}
	host, path, ok := strings.Cut(url, ":")
	// A single letter before the colon is a Windows drive.
	return ok && len(host) > 1 && path != "" && !strings.ContainsAny(host, `/\`) && !strings.HasPrefix(host, ".") && !strings.HasPrefix(host, "~")
}

// withoutCredentials drops the user and password a URL with a scheme may
// carry, such as an access token: tend matches the host and path only. A
// URL that does not parse is dropped entirely.
func withoutCredentials(raw string) string {
	if !strings.Contains(raw, "://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}
