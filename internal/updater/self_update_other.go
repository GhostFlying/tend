//go:build !linux

package updater

import "context"

func PrepareSelfUpdatePlan(context.Context, Config, SelfUpdatePlan) (SelfUpdatePlan, error) {
	return SelfUpdatePlan{}, ErrUnsupported
}
func ApplySelfUpdate(context.Context, Config, SelfUpdatePlan) (SelfUpdateJournal, error) {
	return SelfUpdateJournal{}, ErrUnsupported
}
func SelfUpdateJournalStatus(Config) (SelfUpdateJournal, error) {
	return SelfUpdateJournal{}, ErrUnsupported
}
func AdoptSelfUpdate(context.Context, *Supervisor) (bool, error) { return false, ErrUnsupported }
func (s *Supervisor) selfStatus(context.Context) (SelfUpdateStatus, error) {
	return SelfUpdateStatus{}, ErrUnsupported
}
func (s *Supervisor) requestSelf(context.Context, SelfUpdateRequest) (SelfUpdateStatus, error) {
	return SelfUpdateStatus{}, ErrUnsupported
}
