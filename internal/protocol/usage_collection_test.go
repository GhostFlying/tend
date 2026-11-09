package protocol

import (
	"encoding/json"
	"testing"
)

func TestCollectionBudgetSurvivesRestartAndDoesNotPretendCoverage(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		c := NewUsageCollection(false, stopped)
		c.Failed(1000, stopped, "history_unavailable")
		deadline := c.RetryDeadlineAt
		for now := int64(2000); now < deadline; now += 30000 {
			raw, _ := json.Marshal(c)
			var restarted UsageCollection
			if err := json.Unmarshal(raw, &restarted); err != nil {
				t.Fatal(err)
			}
			c = restarted
			c.Failed(now, stopped, "history_unavailable")
			if c.RetryDeadlineAt != deadline {
				t.Fatal("retry renewed budget")
			}
		}
		c.Failed(deadline, stopped, "history_unavailable")
		if c.Reading() || c.Due(deadline+3600000) {
			t.Fatal("terminal collection still reads", c)
		}
		if stopped && c.RetirementReason != "unavailable" || !stopped && c.State != UsageNeedsReconcile {
			t.Fatal(c)
		}
	}
}

func TestCollectionProgressAndNewExecutionHaveSeparateEpochs(t *testing.T) {
	c := NewUsageCollection(true, false)
	if c.Reading() {
		t.Fatal(c)
	}
	c.Activate("original", "writer")
	c.Failed(1000, false, "incomplete_record")
	c.ReadSucceeded(2000, true, false, false, false)
	if c.RetryDeadlineAt != 0 || c.Evidence != UsagePossible {
		t.Fatal(c)
	}
	c.Stopped(3000, "owned-writer", true)
	if c.RetirementReason == "empty" {
		t.Fatal("attempted dispatch became empty")
	}
	c.ReadSucceeded(4000, false, true, true, true)
	if c.RetirementReason != "complete" {
		t.Fatal(c)
	}
	epoch := c.Epoch
	c.Activate("next", "new-writer")
	if c.Epoch <= epoch || c.StopProof != "" || c.RetirementReason != "" {
		t.Fatal(c)
	}
}
