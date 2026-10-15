package gitws

import (
	"errors"
	"testing"
)

func TestResolveBase(t *testing.T) {
	remote := Remote{Branches: map[string]string{"main": "m", "master": "ms", "develop": "d", "release/3.2": "r"}, Head: "develop"}
	known := func(sha string) bool { return sha == "c0ffee" }
	cases := []struct {
		name      string
		in        BaseIntent
		remote    Remote
		sha, from string
		err       error
	}{
		{"explicit commit", BaseIntent{Commit: "c0ffee"}, remote, "c0ffee", "commit", nil},
		{"missing commit", BaseIntent{Commit: "dead"}, remote, "", "", ErrBaseNotFound},
		{"explicit branch", BaseIntent{Branch: "release/3.2"}, remote, "r", "branch:release/3.2", nil},
		{"missing branch does not fall back", BaseIntent{Branch: "nope", Candidates: []string{"main"}}, remote, "", "", ErrBaseNotFound},
		{"candidate first", BaseIntent{Candidates: []string{"gone", "master"}}, remote, "ms", "candidate:master", nil},
		{"remote head before defaults", BaseIntent{}, remote, "d", "remote_head:develop", nil},
		{"main by default", BaseIntent{}, Remote{Branches: map[string]string{"main": "m", "master": "ms"}}, "m", "default:main", nil},
		{"master when no main", BaseIntent{}, Remote{Branches: map[string]string{"master": "ms"}}, "ms", "default:master", nil},
		{"nothing to start from", BaseIntent{}, Remote{Branches: map[string]string{"x": "x"}}, "", "", ErrBaseNotFound},
		{"stale remote head", BaseIntent{}, Remote{Branches: map[string]string{"main": "m"}, Head: "gone"}, "m", "default:main", nil},
	}
	for _, tc := range cases {
		sha, from, err := ResolveBase(tc.in, tc.remote, known)
		if sha != tc.sha || from != tc.from || !errors.Is(err, tc.err) {
			t.Errorf("%s: ResolveBase = %q, %q, %v; want %q, %q, %v", tc.name, sha, from, err, tc.sha, tc.from, tc.err)
		}
	}
}
