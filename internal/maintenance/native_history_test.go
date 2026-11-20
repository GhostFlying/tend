package maintenance

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/gorilla/websocket"
)

func TestMaintenanceReconcilesAuditAgainstBoundedNativeHistory(t *testing.T) {
	for _, turnID := range []string{"current-turn", ""} {
		t.Run(fmt.Sprintf("known_turn=%t", turnID != ""), func(t *testing.T) {
			c, _, cfg, _ := fixture(t)
			socket := testkit.SocketPath(t, "native.sock")
			const oldTurnCount = 33
			large := strings.Repeat("x", (1 << 20))
			input := map[string]string{"type": "userMessage", "clientId": "current-input"}
			oldItem := map[string]string{"type": "agentMessage", "text": large}
			var fullReads, boundedReads atomic.Int64
			listenAuditFixture(t, socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					var request struct {
						ID     *int
						Method string
						Params struct {
							IncludeTurns  bool
							ThreadID      string
							Cursor        string
							Limit         int
							SortDirection string
							ItemsView     string
						}
					}
					if conn.ReadJSON(&request) != nil {
						return
					}
					if request.ID == nil {
						continue
					}
					result := map[string]any{}
					switch request.Method {
					case "thread/read":
						thread := map[string]any{"status": map[string]string{"type": "idle"}}
						result["thread"] = thread
						if request.Params.IncludeTurns {
							fullReads.Add(1)
							turns := make([]map[string]any, 0, oldTurnCount+1)
							for i := range oldTurnCount {
								turns = append(turns, map[string]any{"id": fmt.Sprintf("old-%d", i), "status": "completed", "items": []any{oldItem}})
							}
							thread["turns"] = append(turns, map[string]any{"id": "current-turn", "status": "completed", "items": []any{input}})
						}
					case "thread/turns/list":
						boundedReads.Add(1)
						p := request.Params
						if p.ThreadID != "reused" || p.SortDirection != "desc" ||
							(turnID != "" && (p.ItemsView != "notLoaded" || p.Limit != 16)) ||
							(turnID == "" && (p.ItemsView != "summary" || p.Limit != 1)) {
							t.Errorf("unexpected bounded turn query: %+v", p)
							return
						}
						if p.Cursor == "" {
							result["data"] = []any{map[string]any{"id": "different-newer-turn", "status": "completed"}}
							result["nextCursor"] = "current-page"
						} else {
							turn := map[string]any{"id": "current-turn", "status": "completed"}
							if turnID == "" {
								turn["items"] = []any{input}
							}
							result["data"] = []any{turn}
						}
					}
					if conn.WriteJSON(map[string]any{"id": request.ID, "result": result}) != nil {
						return
					}
				}
			}))
			n := &NativeClient{}
			c.opts.Native = n
			backend := cfg.Backend
			backend.Socket = socket
			// The pre-change path really exceeds the transport limit.
			if _, err := n.Read(t.Context(), backend, "reused", ReadFullHistory); !errors.Is(err, websocket.ErrReadLimit) {
				t.Fatalf("full-history negative control did not hit the transport limit: %v", err)
			}
			c.state.Active = &protocol.MaintenanceSession{
				ID: "current-audit", Phase: "running", Backend: backend, SessionID: "reused",
				InputID: "current-input", TurnID: turnID, ScheduledAuditID: "timer",
				SessionProfile: "fixture", Audit: &protocol.MaintenanceAudit{Through: 10},
			}
			if err := c.save(t.Context()); err != nil {
				t.Fatal(err)
			}
			step(t, c)
			s := c.Status()
			history, _, _, err := c.History(t.Context(), 0, 10)
			if s.Active != nil || s.AuditedThrough != 10 || len(history) != 1 || err != nil ||
				history[0].InputID != "current-input" || history[0].TurnID != "current-turn" ||
				fullReads.Load() != 1 || boundedReads.Load() != 2 {
				t.Fatalf("current turn did not reconcile using only bounded reads: state=%+v history=%+v error=%v", s, history, err)
			}
		})
	}
}

func TestMaintenanceLegacyNativeReadDoesNotEnableAuditReuse(t *testing.T) {
	for _, legacyVariant := range []bool{false, true} {
		t.Run(fmt.Sprintf("codex_variant=%t", legacyVariant), func(t *testing.T) {
			testMaintenanceLegacyNativeRead(t, legacyVariant)
		})
	}
}

func testMaintenanceLegacyNativeRead(t *testing.T, legacyVariant bool) {
	c, _, _, _ := fixture(t)
	socket := testkit.SocketPath(t, "native.sock")
	c.state.Backend.Socket = socket
	now := c.opts.Now()
	c.opts.Now = func() time.Time { return now }
	native := &auditNativeFixture{
		threads: map[string][]NativeTurn{
			"legacy": {{ID: "completed-turn", InputID: "input", Status: "completed"}},
		},
		creates: 1, legacyRead: true, legacyVariant: legacyVariant,
	}
	listenAuditFixture(t, socket, http.HandlerFunc(native.serve))
	c.opts.Native = &NativeClient{}
	_, profile, err := c.auditThreadProfile()
	if err != nil {
		t.Fatal(err)
	}
	c.state.Active = &protocol.MaintenanceSession{
		ID: "audit", Phase: "running", Backend: c.state.Backend, SessionID: "legacy",
		InputID: "input", TurnID: "completed-turn", ScheduledAuditID: "timer",
		SessionProfile: profile, Audit: &protocol.MaintenanceAudit{Through: 12},
	}
	c.enqueue("repair after audit")
	step(t, c)
	s := c.Status()
	if s.Active == nil || s.Active.HistoryRead != nativeHistoryLegacy || s.AuditedThrough != 12 || s.IdleAuditSession != nil {
		t.Fatalf("legacy provider did not finish and record its read capability: %+v", s)
	}
	step(t, c)
	repair := c.Status().Active
	if repair.SessionID != "legacy" || repair.InputID == "" || repair.ScheduledAuditID != "" {
		t.Fatalf("original repair did not stay on the existing legacy thread: %+v", repair)
	}
	// Queue the next timer while the original repair is still active.
	now = now.Add(2 * time.Hour)
	command(t, c, "audit", protocol.MaintenanceRequest{ScheduledAudit: true})
	next := c.Status().PendingAudits[0]
	native.complete("legacy")
	for range 4 {
		step(t, c)
	}
	a := c.Status().Active
	if a == nil || a.SessionID == "legacy" || a.ScheduledAuditID != next {
		t.Fatalf("subsequent timer did not get its own thread: %+v", a)
	}
}

func TestNativeUnknownTurnListDoesNotMaskOtherRejections(t *testing.T) {
	for _, rejection := range []nativeRPCError{
		{Code: -32600, Message: "Invalid request: missing field `threadId`"},
		{Code: -32600, Message: "Invalid request: unknown variant `notLoaded`, expected `full`"},
		{Code: -32600, Message: "Invalid request: unknown variant `thread/turns/list-other`, expected `initialize`"},
		{Code: -32603, Message: "Invalid request: unknown variant `thread/turns/list`"},
	} {
		if rejection.unknownTurnList() {
			t.Fatalf("ordinary rejection treated as unsupported method: %+v", rejection)
		}
	}
}
