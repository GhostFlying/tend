//go:build !linux

package updater

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
)

type HandoffPlan struct {
	Recovery protocol.ForwardRecoveryPlan `json:"recovery"`
}
type HandoffJournal struct {
	Plan  HandoffPlan `json:"plan"`
	Phase string      `json:"phase"`
}

func PlanHandoff(context.Context, Config, HandoffPlan) error { return ErrUnsupported }
func ApplyHandoff(context.Context, Config, HandoffPlan) (HandoffJournal, error) {
	return HandoffJournal{}, ErrUnsupported
}
func HandoffStatus(context.Context, Config) (HandoffJournal, error) {
	return HandoffJournal{}, ErrUnsupported
}
func AdoptPendingHandoff(context.Context, *Supervisor) error { return ErrUnsupported }

func PrepareHandoffPlan(context.Context, Config, HandoffPlan) (HandoffPlan, error) {
	return HandoffPlan{}, ErrUnsupported
}

func PlanHandoffResume(context.Context, Config, HandoffPlan, string, uint64) (string, error) {
	return "", ErrUnsupported
}
func ResumeHandoff(context.Context, Config, HandoffPlan, string, uint64) (HandoffJournal, error) {
	return HandoffJournal{}, ErrUnsupported
}
