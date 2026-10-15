package netx

import (
	"context"
	"errors"
	"testing"
)

func TestMatches(t *testing.T) {
	id := Identity{Node: "build-box", Tags: []string{"tag:tend-runner"}}
	cases := map[string]struct {
		allowed []string
		want    bool
	}{
		"tag":         {[]string{"tag:tend-runner"}, true},
		"node":        {[]string{"build-box"}, true},
		"other tag":   {[]string{"tag:tend-server"}, false},
		"other node":  {[]string{"laptop"}, false},
		"tag as node": {[]string{"tend-runner"}, false},
		"empty":       {nil, false},
	}
	for name, tc := range cases {
		if got := id.Matches(tc.allowed); got != tc.want {
			t.Errorf("%s: Matches(%v) = %v, want %v", name, tc.allowed, got, tc.want)
		}
	}
}

func TestStatic(t *testing.T) {
	if _, err := (Static{}).WhoIs(context.Background(), "1.2.3.4:5"); !errors.Is(err, ErrUnknownPeer) {
		t.Errorf("empty Static: %v", err)
	}
	id := Identity{Node: "n"}
	if got, err := (Static{Identity: &id}).WhoIs(context.Background(), "1.2.3.4:5"); err != nil || got.Node != "n" {
		t.Errorf("Static = %+v, %v", got, err)
	}
}
