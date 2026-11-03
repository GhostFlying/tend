//go:build !linux

package updater

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
)

type CommittedHandoffPlan struct {
	Maintenance protocol.CommittedMaintenancePlan `json:"maintenance"`
}
type CommittedHandoffJournal struct{}

func ReadCommittedHandoffPlan(string) (CommittedHandoffPlan, error) {
	return CommittedHandoffPlan{}, ErrUnsupported
}
func PrepareCommittedHandoffPlan(context.Context, Config, CommittedHandoffPlan) (CommittedHandoffPlan, error) {
	return CommittedHandoffPlan{}, ErrUnsupported
}
func ApplyCommittedHandoff(context.Context, Config, CommittedHandoffPlan) (CommittedHandoffJournal, error) {
	return CommittedHandoffJournal{}, ErrUnsupported
}
func CommittedHandoffStatus(context.Context, Config) (CommittedHandoffJournal, error) {
	return CommittedHandoffJournal{}, ErrUnsupported
}

func ResumeCommittedHandoff(context.Context, Config, CommittedHandoffPlan, string, uint64) (CommittedHandoffJournal, error) {
	return CommittedHandoffJournal{}, ErrUnsupported
}
