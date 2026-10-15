package app

import "sync"

// Broadcast wakes every waiter at once. Waiters take the channel from Wait
// before they look at the ledger and then block on it, so a change
// committed in between is not missed.
type Broadcast struct {
	mu sync.Mutex
	ch chan struct{}
}

// NewBroadcast returns a Broadcast.
func NewBroadcast() *Broadcast { return &Broadcast{ch: make(chan struct{})} }

// Notify wakes everyone waiting. It implements Notifier.
func (b *Broadcast) Notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	close(b.ch)
	b.ch = make(chan struct{})
}

// Wait returns a channel that is closed by the next Notify.
func (b *Broadcast) Wait() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ch
}
