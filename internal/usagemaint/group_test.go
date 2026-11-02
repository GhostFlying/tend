package usagemaint

import (
	"fmt"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestPreviewKeepsCrossFileConflictsAndResponseOverlapsTogether(t *testing.T) {
	var samples []protocol.TokenUsageSample
	for i := range 300 {
		samples = append(samples, protocol.TokenUsageSample{Source: "native", Provider: "codex", ThreadID: "thread", TurnID: "turn", RecordID: fmt.Sprint(i), RequestID: fmt.Sprint(i)})
	}
	// A copied identity with changed content, and another native ordinal using
	// the same legacy response, must be checked in the same rollback preview.
	variant := samples[0]
	variant.NativeContentHash = "different"
	samples = append(samples, variant)
	overlap := samples[299]
	overlap.RequestID = samples[0].RequestID
	samples = append(samples, overlap)
	batches := previewGroups(samples)
	if len(batches) != 2 {
		t.Fatal(len(batches))
	}
	count := 0
	for _, sample := range batches[0] {
		if sample.RecordID == "0" || sample.RecordID == "299" {
			count++
		}
	}
	if count != 4 {
		t.Fatalf("split connected identities across previews: %d", count)
	}
}
