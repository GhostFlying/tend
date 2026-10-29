package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestArchiveShutdownWaitsAfterObservableDeadlineWithoutCancellingReader(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		if r.Context().Err() != nil {
			t.Error("accepted reader was cancelled")
		}
		_, _ = io.WriteString(w, "original")
	}))
	server.Start()
	defer server.Close()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		res, err := http.Get(server.URL)
		if err != nil {
			t.Error(err)
			return
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if string(data) != "original" {
			t.Error("reader truncated")
		}
	}()
	<-entered
	done := make(chan error, 1)
	observed := make(chan struct{}, 1)
	log := slog.New(&archiveShutdownLog{observed: observed})
	go func() { done <- shutdownHTTP(context.Background(), server.Config, 10*time.Millisecond, log) }()
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("shutdown failure not observed")
	}
	select {
	case err := <-done:
		t.Fatal("shutdown returned before accepted reader finished", err)
	default:
	}
	close(release)
	<-requestDone
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("original timeout disappeared", err)
	}
}

type archiveShutdownLog struct{ observed chan struct{} }

func (h *archiveShutdownLog) Enabled(context.Context, slog.Level) bool { return true }
func (h *archiveShutdownLog) Handle(context.Context, slog.Record) error {
	select {
	case h.observed <- struct{}{}:
	default:
	}
	return nil
}
func (h *archiveShutdownLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *archiveShutdownLog) WithGroup(string) slog.Handler      { return h }
