//go:build linux

package upgradectl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestReceiptRecoveryStartupRequiresExactPersistedAuthority(t *testing.T) {
	ids := protocol.NewULIDGen(time.Now, nil)
	p := protocol.CommittedMaintenancePlan{RequestID: ids.New(protocol.PrefixRequest), LinkRequestID: ids.New(protocol.PrefixRequest), PreparationRevision: 1, ReceiptRecoveryStop: &protocol.CommittedReceiptRecoveryStop{}}
	raw, err := json.Marshal(p.ReceiptRecoveryStop.Runs)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	sum := hex.EncodeToString(hash[:])
	request := ids.New(protocol.PrefixRequest)
	newSource := func() updater.CommittedMaintenance {
		return updater.CommittedMaintenance{Plan: p, ReceiptRecoveryAuthorized: true, ReceiptRecoveryRequestID: request, ReceiptRecoveryFirstSHA256: sum, ReceiptRecoverySecondSHA256: sum, ReceiptRecoveryAuthorization: &protocol.MaintenanceReceiptRecoveryRequest{RequestID: request, ExpectedGeneration: 1}, ArchiveAuthorization: &protocol.ArchiveOnlineStatus{Preparation: protocol.ArchivePreparation{Prepared: true, Revision: 1}, HostedDrained: false, Blockers: []string{"native receipt reads: 1", "hosted operations are active or uncertain"}}}
	}
	if err := verifyReceiptRecoveryAuthorization(newSource()); err != nil {
		t.Fatal("exact authority rejected", err)
	}
	for _, mutation := range []string{"missing_authority", "changed_request", "reused_request", "changed_capture", "changed_digest", "new_effect", "false_drain", "ordinary_scope"} {
		t.Run(mutation, func(t *testing.T) {
			source := newSource()
			switch mutation {
			case "missing_authority":
				source.ReceiptRecoveryAuthorized = false
			case "changed_request":
				source.ReceiptRecoveryAuthorization.RequestID = ids.New(protocol.PrefixRequest)
			case "reused_request":
				source.ReceiptRecoveryRequestID = p.RequestID
			case "changed_capture":
				source.ReceiptRecoveryAuthorization.SecondCapture = []protocol.CommittedRetainedRun{{TaskID: "different"}}
			case "changed_digest":
				source.ReceiptRecoveryFirstSHA256 = hex.EncodeToString(make([]byte, 32))
				source.ReceiptRecoverySecondSHA256 = source.ReceiptRecoveryFirstSHA256
			case "new_effect":
				source.ArchiveAuthorization.Blockers = append(source.ArchiveAuthorization.Blockers, "native steers: 1")
			case "false_drain":
				source.ArchiveAuthorization.HostedDrained = true
			case "ordinary_scope":
				source.Plan.ReceiptRecoveryStop = nil
			}
			if err := verifyReceiptRecoveryAuthorization(source); err == nil {
				t.Fatal("unproven recovery authority accepted")
			}
		})
	}
}
