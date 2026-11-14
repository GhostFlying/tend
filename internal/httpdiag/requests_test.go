package httpdiag

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type synchronizedLog struct {
	sync.Mutex
	b bytes.Buffer
}

func (b *synchronizedLog) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.b.Write(p)
}
func (b *synchronizedLog) String() string { b.Lock(); defer b.Unlock(); return b.b.String() }

func TestShutdownSnapshotLocatesWriteWithoutCancellingOrLeakingInput(t *testing.T) {
	var output synchronizedLog
	tracker := New(time.Now, func() string { return "server-trace" }, slog.New(slog.NewJSONHandler(&output, nil)))
	entered, release := make(chan struct{}), make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /writes/{id}", func(w http.ResponseWriter, r *http.Request) {
		Identity(r.Context(), "original-request", "original-trace", "write_original")
		close(entered)
		<-release
		if r.Context().Err() != nil {
			t.Error("accepted write cancelled")
		}
		_, _ = io.WriteString(w, "committed")
	})
	server := httptest.NewServer(tracker.Wrap(Routes(mux)))
	defer server.Close()
	done := make(chan string, 1)
	go func() {
		r, _ := http.NewRequest(http.MethodPost, server.URL+"/writes/private-id?token=query-secret", strings.NewReader("body-secret"))
		r.Header.Set("Authorization", "Bearer header-secret")
		response, err := server.Client().Do(r)
		if err != nil {
			done <- err.Error()
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		done <- string(body)
	}()
	<-entered
	tracker.Snapshot(t.Context(), "drain_timeout")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := server.Config.Shutdown(ctx); err == nil {
		t.Error("pending write disappeared")
	}
	text := output.String()
	for _, wanted := range []string{"POST /writes/{id}", "original-request", "original-trace", "write_original", "\"active_requests\":1", "\"request_kind\":\"write\""} {
		if !strings.Contains(text, wanted) {
			t.Error("missing shutdown diagnostic", wanted, text)
		}
	}
	for _, secret := range []string{"private-id", "query-secret", "body-secret", "header-secret"} {
		if strings.Contains(text, secret) {
			t.Error("diagnostic exposed request input", secret)
		}
	}
	close(release)
	if result := <-done; result != "committed" {
		t.Fatal(result)
	}
	tracker.Snapshot(t.Context(), "drain_finished")
	if !strings.Contains(output.String(), "\"active_requests\":0") {
		t.Fatal("completed request retained")
	}
}
