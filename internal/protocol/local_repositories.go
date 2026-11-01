package protocol

// LocalRepositoryBinding identifies an existing local provider repository.
// The private operator socket verifies it before publishing the binding.
type LocalRepositoryBinding struct {
	Repo      string `json:"repo"`
	RemoteURL string `json:"remote_url"`
	Orca      string `json:"orca,omitempty"`
}

// BindLocalRepositoryRequest retains identity and CAS across operator retries.
type BindLocalRepositoryRequest struct {
	RequestID        string                 `json:"request_id"`
	ExpectedRevision int64                  `json:"expected_control_revision"`
	Binding          LocalRepositoryBinding `json:"binding"`
}

// LocalRepositoryStatus is a safe observation without local provider paths.
type LocalRepositoryStatus struct {
	Revision     int64              `json:"control_revision"`
	Repositories []RunnerRepository `json:"repositories"`
	Error        string             `json:"error,omitempty"`
}
