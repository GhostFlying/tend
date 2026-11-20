package protocol

// tend:compat renewal-bound-runs protocol=6 remove=8

// RunnerOwnsExecution identifies the execution contract frozen into the Run.
// Older persisted records keep their original lease and recovery behavior.
func RunnerOwnsExecution(version int64) bool { return version >= 7 }

const (
	EventWorkerSubmitted = "worker.result_submitted"
	EventWorkerOutcome   = "worker.outcome"
	EventWorkerStopped   = "worker.physical_exit"
)
