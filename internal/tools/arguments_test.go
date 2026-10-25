package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
)

func TestUnknownArgumentContainsPathAndHostCorrection(t *testing.T) {
	var args struct {
		Spec domain.Spec `json:"spec"`
	}
	err := decode(json.RawMessage(`{"spec":{"workspace":{"host":"machine"}}}`), &args)
	if err == nil {
		t.Fatal("accepted unknown field")
	}
	for _, want := range []string{"spec.workspace.host", "allowed fields:", "spec.requirements.host_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %s: %v", want, err)
		}
	}
	if err := decode(json.RawMessage(`{"spec":{"requirements":{"host_id":"machine"}}}`), &args); err != nil {
		t.Fatal(err)
	}
	if args.Spec.Requirements.HostID != "machine" {
		t.Fatal("host lost")
	}
}
