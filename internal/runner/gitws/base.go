package gitws

import (
	"slices"

	"github.com/GhostFlying/tend/internal/runner"
)

// ErrBaseNotFound means the base a mount asks for does not exist on the
// remote.
var ErrBaseNotFound = runner.ErrBaseNotFound

// BaseIntent is what a mount asks for as its starting point.
type BaseIntent struct {
	Branch     string
	Commit     string
	Candidates []string
}

// Remote is what a fetch revealed about the remote: its branches by name
// and the branch its HEAD points to, if any.
type Remote struct {
	Branches map[string]string
	Head     string
}

// ResolveBase picks the commit a mount starts from and says where it came
// from. An explicit commit or branch is used as given and never falls
// back. Otherwise the configured candidates are tried in order, then the
// remote's HEAD branch, then main and master. hasCommit reports whether a
// commit is available locally.
func ResolveBase(in BaseIntent, remote Remote, hasCommit func(string) bool) (sha, from string, err error) {
	switch {
	case in.Commit != "":
		if !hasCommit(in.Commit) {
			return "", "", ErrBaseNotFound
		}
		return in.Commit, "commit", nil
	case in.Branch != "":
		sha, ok := remote.Branches[in.Branch]
		if !ok {
			return "", "", ErrBaseNotFound
		}
		return sha, "branch:" + in.Branch, nil
	}
	type candidate struct{ branch, from string }
	var order []candidate
	for _, c := range in.Candidates {
		order = append(order, candidate{c, "candidate:" + c})
	}
	if remote.Head != "" {
		order = append(order, candidate{remote.Head, "remote_head:" + remote.Head})
	}
	order = append(order, candidate{"main", "default:main"}, candidate{"master", "default:master"})
	var tried []string
	for _, c := range order {
		if slices.Contains(tried, c.branch) {
			continue
		}
		tried = append(tried, c.branch)
		if sha, ok := remote.Branches[c.branch]; ok {
			return sha, c.from, nil
		}
	}
	return "", "", ErrBaseNotFound
}
