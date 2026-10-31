package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMessagingWatchOutlivesHTTPTimeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"event_seq\":0}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	c := &client{server: s.URL, http: &http.Client{Timeout: 30 * time.Millisecond}}
	err := messagingWatch(ctx, c, &Env{Stdout: io.Discard}, "/events")
	if ctx.Err() == nil {
		t.Fatalf("watch terminated before caller cancellation: %v", err)
	}
}
