package protocol

import (
	"strconv"
	"strings"
)

const workerFailurePrefix = "worker_failed:"

// WorkerFailureReason retains a terminal provider classification in the
// existing free-form Release reason. Older peers can still settle that Run.
func WorkerFailureReason(failure *AgentFailure) string {
	if failure == nil || failure.WillRetry || !validWorkerFailureCode(failure.Code) || failure.HTTPStatus < 0 || failure.HTTPStatus > 599 {
		return "worker_failed"
	}
	return workerFailurePrefix + failure.Code + ":" + strconv.Itoa(failure.HTTPStatus)
}

func ParseWorkerFailureReason(reason string) *AgentFailure {
	value, ok := strings.CutPrefix(reason, workerFailurePrefix)
	if !ok {
		return nil
	}
	code, status, ok := strings.Cut(value, ":")
	httpStatus, err := strconv.Atoi(status)
	if !ok || err != nil || !validWorkerFailureCode(code) || httpStatus < 0 || httpStatus > 599 {
		return nil
	}
	return &AgentFailure{Code: code, HTTPStatus: httpStatus}
}

func validWorkerFailureCode(code string) bool {
	if code == "" || len(code) > 128 {
		return false
	}
	for _, c := range code {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
