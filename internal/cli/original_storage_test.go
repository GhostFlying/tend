package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestOriginalStorageFreezesCursorRevisionAndRequestIdentity(t *testing.T) {
	e := newTestEnv(t, "http://localhost")
	id := e.IDs.New(protocol.PrefixRequest)
	var original string
	for range 2 {
		e.out.Reset()
		err := runOriginalStorage(context.Background(), e.Env, []string{"import", "--file-id", "retained-original", "--source-key", "native-session", "--after", "1234", "--limit", "50", "--revision", "7", "--request-id", id, "--dry-run"})
		if err != nil {
			t.Fatal(err)
		}
		var envelope protocol.Envelope
		if err = json.Unmarshal(e.out.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		var p protocol.OriginalImport
		if err = json.Unmarshal(envelope.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if envelope.RequestID != id || envelope.Operation != protocol.OpImportOriginals || envelope.ExpectedControlRevision == nil || *envelope.ExpectedControlRevision != 7 || p.After != 1234 || p.FileID != "retained-original" || p.SourceKey != "native-session" || p.Limit != 50 {
			t.Fatal(envelope, p)
		}
		if original != "" && original != e.out.String() {
			t.Fatal("retry input changed")
		}
		original = e.out.String()
	}
	for _, args := range [][]string{{"migrate", "--revision", "7", "--limit", "0"}, {"import", "--revision", "7"}, {"import", "--revision", "7", "--file-id", "f", "--source-key", "s", "--after", "-1"}, {"migrate"}} {
		if err := runOriginalStorage(context.Background(), e.Env, args); err == nil {
			t.Fatal("invalid original control accepted", args)
		}
	}
}
