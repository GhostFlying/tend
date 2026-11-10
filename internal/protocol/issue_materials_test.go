package protocol

import (
	"net/url"
	"testing"
)

func TestIssueMaterialsHistoricalSelectorIsExplicitAndPositive(t *testing.T) {
	const run = "run_01J00000000000000000000000"
	for _, raw := range []string{
		"kind=delivery_authorization&id=" + run + "&duty_revision=3",
		"kind=delivery_authorization&id=" + run,
		"kind=overview",
	} {
		values, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatal(err)
		}
		query, err := ParseIssueMaterialsQuery(values)
		if err != nil || query.Kind == "delivery_authorization" && values.Has("duty_revision") && query.DutyRevision != 3 {
			t.Fatal("valid historical selector was lost", query, err)
		}
	}
	for _, raw := range []string{
		"kind=delivery_authorization&id=" + run + "&duty_revision=0",
		"kind=delivery_authorization&id=" + run + "&duty_revision=-1",
		"kind=delivery_authorization&id=" + run + "&duty_revision=not-a-version",
		"kind=delivery_authorization&id=" + run + "&duty_revision=3&duty_revision=7",
		"kind=overview&duty_revision=3",
		"kind=delivery_authorization&id=task_01J00000000000000000000000&duty_revision=3",
	} {
		values, _ := url.ParseQuery(raw)
		if _, err := ParseIssueMaterialsQuery(values); CodeOf(err) != CodeInvalidRequest {
			t.Fatal("ambiguous or inapplicable selector was accepted", raw, err)
		}
	}
}
