package runner

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBoundedCompletionKeepsNativeLifecycleIdentity(t *testing.T) {
	for _, nested := range []bool{false, true} {
		body := map[string]any{"thread_id": "thread", "turn_id": "turn", "item_id": "call"}
		item := map[string]any{"id": "call", "type": "commandExecution", "status": "completed", "exitCode": 0, "aggregatedOutput": strings.Repeat("界<", maxEventBody)}
		if nested {
			body["kind"] = "item_completed"
			body["data"] = item
		} else {
			for k, v := range item {
				body[k] = v
			}
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		clipped := boundedBody(raw)
		encoded, err := json.Marshal(clipped)
		if err != nil || len(encoded) > maxEventBody {
			t.Fatalf("event exceeded encoded bound: %d, %v", len(encoded), err)
		}
		var got map[string]any
		if err := json.Unmarshal(clipped, &got); err != nil {
			t.Fatal(err)
		}
		if got["thread_id"] != "thread" || got["turn_id"] != "turn" || got["item_id"] != "call" || got["truncated"] != true {
			t.Fatalf("lost lifecycle identity: %s", clipped)
		}
		metadata := got
		if nested {
			metadata = got["data"].(map[string]any)
		}
		if metadata["type"] != "commandExecution" || metadata["status"] != "completed" || metadata["truncated"] != true {
			t.Fatalf("lost completion state: %s", clipped)
		}
		if strings.Contains(string(clipped), "aggregatedOutput") {
			t.Fatal("oversized content retained")
		}
	}
}

func TestBoundedBodyCannotRetainOversizedMetadata(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"thread_id": strings.Repeat("x", maxEventBody+1)})
	if got := boundedBody(raw); len(got) > maxEventBody || !json.Valid(got) {
		t.Fatalf("invalid bounded event: %d", len(got))
	}
}
