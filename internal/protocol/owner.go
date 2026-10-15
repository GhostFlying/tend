package protocol

// Owner API wire types besides the envelope and the read views.

// SessionRequest creates an owner session for a client. The client also
// sends ClientID in the X-Tend-Client header.
type SessionRequest struct {
	ClientID string `json:"client_id"`
}

// SessionView is the caller's session.
type SessionView struct {
	Login    string `json:"login"`
	ClientID string `json:"client_id"`
}
