package runnerapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestShutdownEndsMessageAndQuestionWaitsWithoutConsumingFacts(t *testing.T) {
	for _, kind := range []string{"messages", "questions"} {
		t.Run(kind, func(t *testing.T) {
			e, assignment := httpQuestionRun(t, protocol.CapabilityRunMessages)
			path := "/runner/v1/messages?wait=25"
			var question protocol.WorkerQuestion
			if kind == "questions" {
				raw := e.mustOK(asRun(*assignment), e.envelope(protocol.OpRunsAskOwner, assignment.RunID, protocol.AskOwnerPayload{QuestionKey: "pending", Question: "Keep the original question open?"}))
				if err := json.Unmarshal(raw, &question); err != nil {
					t.Fatal(err)
				}
				path = "/runner/v1/questions/" + question.DecisionID + "?wait=25"
			}
			stopping, entered := make(chan struct{}), make(chan struct{})
			s := New(e.app, e.peers, secret, Config{Stopping: stopping}, slog.Default())
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); s.Handler().ServeHTTP(w, r) }))
			defer server.Close()
			defer server.CloseClientConnections()
			result := make(chan error, 1)
			go func() {
				request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
				credential := asRun(*assignment)
				request.Header.Set(credential.header, credential.subject)
				request.Header.Set("Authorization", "Bearer "+credential.token)
				response, err := server.Client().Do(request)
				if err != nil {
					result <- err
					return
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Error(response.StatusCode)
				}
				if kind == "questions" {
					var got protocol.WorkerQuestion
					err = json.NewDecoder(response.Body).Decode(&got)
					if got.DecisionID != question.DecisionID || got.Status != "open" {
						t.Error("shutdown changed question", got)
					}
				} else {
					var got protocol.RunMessagePage
					err = json.NewDecoder(response.Body).Decode(&got)
					if len(got.Messages) != 0 {
						t.Error("shutdown fabricated a message", got)
					}
				}
				result <- err
			}()
			<-entered
			close(stopping)
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("read wait held shutdown")
			}
		})
	}
}

func TestShutdownEndsIdleClaimWithoutCreatingWork(t *testing.T) {
	e := newEnv(t, 25*time.Second)
	stopping := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	s := New(e.app, e.peers, secret, Config{ClaimWait: 25 * time.Second, Stopping: stopping}, slog.Default())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		s.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(server.CloseClientConnections)
	e.server = server
	done := make(chan *protocol.Assignment, 1)
	go func() { done <- e.claim(e.envelope(protocol.OpRunnerClaim, "", map[string]any{})) }()
	<-entered
	close(stopping)
	select {
	case assignment := <-done:
		if assignment != nil {
			t.Fatal("shutdown created an assignment", assignment)
		}
	case <-time.After(time.Second):
		t.Fatal("idle claim held shutdown until its 25-second timer")
	}
}
