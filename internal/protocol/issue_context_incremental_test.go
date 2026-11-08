package protocol

import (
	"net/url"
	"testing"
)

func TestIssueContextQueryLegacyAliasAndIncrementalAreDistinct(t *testing.T) {
	for _, wire := range []string{"after=100", "after_source=100", "after=100&after_source=100"} {
		q, err := ParseIssueContextQuery(mustContextQuery(t, wire))
		if err != nil || q.AfterSource == nil || *q.AfterSource != 100 {
			t.Fatal(wire, q, err)
		}
	}
	for _, wire := range []string{"after=100&after_source=101", "after=not-a-sequence", "after_source=-1", "view=incremental&after=0", "view=incremental&after_source=0", "cursor=opaque", "limit=1", "view=incremental&limit=0", "view=incremental&limit=101", "view=incremental&limit=2&limit=3", "after_revision=2"} {
		if _, err := ParseIssueContextQuery(mustContextQuery(t, wire)); err == nil {
			t.Fatal("accepted invalid mixed query", wire)
		}
	}
	q, err := ParseIssueContextQuery(mustContextQuery(t, "view=incremental"))
	if err != nil || q.Limit != 50 || q.AfterSource != nil {
		t.Fatal(q, err)
	}
	q, err = ParseIssueContextQuery(mustContextQuery(t, "view=incremental&cursor=opaque&limit=7"))
	if err != nil || q.Cursor != "opaque" || q.Limit != 7 {
		t.Fatal(q, err)
	}
}
func mustContextQuery(t *testing.T, s string) url.Values {
	t.Helper()
	v, e := url.ParseQuery(s)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
