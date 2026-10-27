package orca

import (
	"crypto/sha256"
	"fmt"
)

func (w *worker) recordTakeover(a Agent, reason string) {
	w.holding = reason
	w.holdingEvidence = map[string]any{
		"reason":                    reason,
		"expected_session_id":       w.st.SessionID,
		"expected_prompt_sha256":    fmt.Sprintf("%x", sha256.Sum256([]byte(w.st.Said))),
		"observed_prompt_sha256":    fmt.Sprintf("%x", sha256.Sum256([]byte(said(a.Prompt)))),
		"observed_state_started_at": a.StateStartedAt,
	}
	if a.ProviderSession != nil {
		w.holdingEvidence["observed_session_id"] = a.ProviderSession.ID
	}
}
