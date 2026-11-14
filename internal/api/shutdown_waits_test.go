package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestShutdownEndsRunLogWaitWithCurrentPage(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	e.configure()
	run := e.startRun(e.task("retain original result"))
	done := make(chan int, 1)
	go func() { status, _ := e.runLogs(token, run, 0, 20); done <- status }()
	e.server.EndStreams()
	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Fatal(status)
		}
	case <-time.After(time.Second):
		t.Fatal("re-readable log wait held shutdown")
	}
}
