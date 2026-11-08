package cli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueContextCLIIncrementalPreservesPagesAndParameters(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	want := protocol.IssueContextIncremental{IssueID: "selected", Kind: "delta", From: 10, Through: 20, Items: []protocol.IssueContextChange{{Kind: "source", ID: "original", Revision: 3, Removed: true}}, More: true, NextCursor: "next/+=cursor", ContentID: "same-content"}
	f.on("GET /api/v1/issues/selected/context", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		q := r.URL.Query()
		if q.Get("view") != "incremental" || q.Get("cursor") != "previous/+=cursor" || q.Get("limit") != "7" || q.Has("after") || q.Has("after_source") {
			t.Error("lost or mixed context selectors", q)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	if code := e.main("issue", "context", "selected", "--view", "incremental", "--cursor", "previous/+=cursor", "--limit", "7", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var got protocol.IssueContextIncremental
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("CLI lost incremental type fields", got, err)
	}
	for _, tail := range [][]string{{"--view", "incremental", "--after-source", "0"}, {"--view", "incremental", "--after", "0"}, {"--view", "incremental", "--limit", "0"}, {"--after", "2", "--after-source", "3"}, {"--cursor", "opaque"}} {
		if code := e.main(append([]string{"issue", "context", "selected"}, tail...)...); code != exitUsage {
			t.Fatal("invalid query sent", tail, code)
		}
	}
	if len(f.requests) != 1 {
		t.Fatal("invalid reads performed HTTP", f.requests)
	}
}
