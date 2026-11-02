package usagemaint

import (
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// previewGroups keeps every version of a native identity and every synthetic
// legacy-response overlap in one rollback preview, even across source files.
func previewGroups(samples []protocol.TokenUsageSample) [][]protocol.TokenUsageSample {
	parent := make([]int, len(samples))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	identities := map[string]int{}
	for i, s := range samples {
		for _, key := range []string{"native\x00" + s.Source + "\x00" + s.RecordID, "legacy\x00" + strings.Join([]string{s.Provider, s.ThreadID, s.TurnID, s.RequestID}, "\x00")} {
			if previous, ok := identities[key]; ok {
				parent[find(i)] = find(previous)
			} else {
				identities[key] = i
			}
		}
	}
	groups := map[int][]protocol.TokenUsageSample{}
	order := []int{}
	for i, s := range samples {
		root := find(i)
		if _, ok := groups[root]; !ok {
			order = append(order, root)
		}
		groups[root] = append(groups[root], s)
	}
	var batches [][]protocol.TokenUsageSample
	batch := []protocol.TokenUsageSample{}
	for _, root := range order {
		group := groups[root]
		if len(batch) > 0 && len(batch)+len(group) > 256 {
			batches = append(batches, batch)
			batch = nil
		}
		batch = append(batch, group...)
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}
	return batches
}
