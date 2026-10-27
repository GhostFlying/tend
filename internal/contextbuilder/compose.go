package contextbuilder

import "github.com/GhostFlying/tend/internal/protocol"

// ComposeRequest asks for the context of an agent session.
type ComposeRequest struct {
	// Self is the hosted coordinator identity.
	Self protocol.Principal
	// Full asks for a full snapshot; otherwise the context is a delta of
	// the changes after Watermark, or nothing. A full snapshot also tells
	// the results after Watermark, when there is one: the snapshot lists
	// only unfinished tasks, and the session has not heard of those
	// results.
	Full      bool
	Epoch     int64
	Watermark int64
	PacketID  string
}

// Composed is a session's context and the ledger position it reflects.
type Composed struct {
	// Text is the packet; empty when there is nothing to tell.
	Text string
	// ToSeq is the last event the context reflects.
	ToSeq int64
}
