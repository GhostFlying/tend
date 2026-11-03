package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUpgradeManagerParkReadsLegacyStateWithoutEnablingFence(t *testing.T) {
	const legacy = `{"view":{"upgrade_id":"upgrade-old","status":"running"},"hosts":[]}`
	var row UpgradeRow
	if err := json.Unmarshal([]byte(legacy), &row); err != nil {
		t.Fatal(err)
	}
	if row.ParkIdleManagers || len(row.ManagerParks) != 0 {
		t.Fatal("legacy state acquired park intent", row)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "park_idle_managers") || strings.Contains(string(raw), "manager_parks") {
		t.Fatal("legacy serialization acquired park fields", string(raw))
	}
	row.ParkIdleManagers = true
	row.ManagerParks = []UpgradeManagerPark{{TaskID: "task-old", RunID: "run-old", LeaseID: "lease-old", CloseRequestID: "request-old"}}
	raw, err = json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var restored UpgradeRow
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.ParkIdleManagers || len(restored.ManagerParks) != 1 || restored.ManagerParks[0] != row.ManagerParks[0] {
		t.Fatal("durable park identity changed", restored)
	}
}
