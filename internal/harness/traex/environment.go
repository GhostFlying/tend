package traex

import (
	"context"
	"os"
	"time"
)

func (s *Session) EnvironmentRequiresOriginalThread() bool {
	return s.environmentReference != nil && s.environmentReference.ResumeOnly
}

// A process handshake alone does not prove the original thread can be loaded.
// Keep its rollback reference until thread attachment has actually succeeded.
func (s *Session) environmentThreadAttached(ctx context.Context) error {
	if s.environmentReference == nil {
		return nil
	}
	s.mu.Lock()
	if s.environmentAttached {
		s.mu.Unlock()
		return nil
	}
	// A failed persistence receipt must not be interpreted as a known failed
	// native attachment when the caller later closes the session.
	s.environmentAttached = true
	s.mu.Unlock()
	return s.environmentReference.Record(ctx, "ready")
}

func (s *Session) EnvironmentPending() bool {
	if s.environmentReference == nil {
		return false
	}
	s.mu.Lock()
	if time.Since(s.lastEnvironmentCheck) < 5*time.Second {
		s.mu.Unlock()
		return false
	}
	s.lastEnvironmentCheck = time.Now()
	s.mu.Unlock()
	return !s.environmentReference.Current()
}
func (s *Session) AdoptEnvironment(ctx context.Context) error {
	return s.environmentReference.Adopt(ctx)
}

// EnvironmentQuiescent reads original native facts before idle replacement.
func (s *Session) EnvironmentQuiescent(ctx context.Context, id string) (bool, error) {
	var response struct {
		Thread struct {
			Preview string `json:"preview"`
			Path    string `json:"path"`
			Status  struct {
				Type string `json:"type"`
			} `json:"status"`
			ID string `json:"id"`
		} `json:"thread"`
	}
	// Read live status without hydrating full history: supported native stores
	// can expose live thread state while list_turns is unavailable. The owner
	// separately fences deliveries, native receipts and maintenance operations.
	if err := s.conn.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &response); err != nil {
		return false, err
	}
	if response.Thread.ID != id || response.Thread.Status.Type != "idle" {
		return false, nil
	}
	if s.environmentReference != nil {
		// Native threads without a first persisted input are not resumable
		// after process exit. Leave their update pending until history exists.
		if response.Thread.Preview == "" || response.Thread.Path == "" {
			return false, nil
		}
		info, err := os.Stat(response.Thread.Path)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return false, nil
		}
	}
	s.mu.Lock()
	callbacks := s.toolCallbacks
	s.mu.Unlock()
	s.conn.mu.Lock()
	pending := len(s.conn.pending)
	s.conn.mu.Unlock()
	return callbacks == 0 && pending == 0, nil
}
