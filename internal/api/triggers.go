package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/webhookauth"
)

func triggerQuery(r *http.Request) (protocol.TriggerQuery, error) {
	q := r.URL.Query()
	f := protocol.TriggerQuery{ScheduleID: q.Get("schedule_id"), IssueID: q.Get("issue_id"), Source: q.Get("source"), Status: q.Get("status"), Search: q.Get("search"), After: q.Get("after"), Limit: 50}
	var err error
	if q.Has("limit") {
		f.Limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || f.Limit < 1 || f.Limit > 100 {
			return f, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be 1..100")
		}
	}
	f.Since, err = intParam(q.Get("since"))
	if err != nil || f.Since < 0 {
		return f, protocol.Errorf(protocol.CodeInvalidRequest, "invalid since")
	}
	f.Until, err = intParam(q.Get("until"))
	if err != nil || f.Until < 0 || f.Until > 0 && f.Since > f.Until {
		return f, protocol.Errorf(protocol.CodeInvalidRequest, "invalid until")
	}
	if len(f.Search) > 500 {
		return f, protocol.Errorf(protocol.CodeInvalidRequest, "search is too long")
	}
	return f, nil
}
func (s *Server) triggerList(w http.ResponseWriter, r *http.Request) {
	f, err := triggerQuery(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		switch r.PathValue("kind") {
		case "events":
			return store.TriggerEvents(r.Context(), q, f, s.cfg.Now().UnixMilli())
		case "schedules":
			return store.TriggerSchedules(r.Context(), q, f, s.cfg.Now().UnixMilli())
		case "webhooks":
			return store.ListWebhookSubscriptions(r.Context(), q, f)
		default:
			return nil, protocol.Errorf(protocol.CodeNotFound, "unknown diagnostic collection")
		}
	})
}
func (s *Server) triggerDetail(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		switch r.PathValue("kind") {
		case "events":
			return store.GetTrigger(r.Context(), q, r.PathValue("id"))
		case "schedules":
			return store.GetTriggerSchedule(r.Context(), q, r.PathValue("id"), s.cfg.Now().UnixMilli())
		case "webhooks":
			return store.GetWebhookSubscription(r.Context(), q, r.PathValue("id"))
		default:
			return nil, protocol.Errorf(protocol.CodeNotFound, "unknown diagnostic collection")
		}
	})
}

func (s *Server) triggerSources(w http.ResponseWriter, r *http.Request) {
	after, err := intParam(r.URL.Query().Get("after"))
	if err != nil || after < 0 {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid source cursor"))
		return
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		event, err := store.GetTrigger(r.Context(), q, r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		if event.BatchID == "" {
			return protocol.BackgroundSourcePage{Sources: []protocol.BackgroundSource{}}, nil
		}
		var message string
		if err := q.QueryRowContext(r.Context(), `SELECT message_id FROM background_batches WHERE batch_id=?`, event.BatchID).Scan(&message); err != nil {
			return nil, err
		}
		return store.BackgroundSources(r.Context(), q, message, after)
	})
}
func (s *Server) stageWebhookKey(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.cfg.WebhookKeys == nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "webhook storage is unavailable"))
		return
	}
	key, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err == nil {
		err = s.cfg.WebhookKeys.Put(r.Context(), r.PathValue("id"), key)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key_ref": r.PathValue("id")})
}

// WebhookHandler exposes only the separately authenticated external intake.
func (s *Server) WebhookHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/v1/{id}", s.receiveWebhook)
	return mux
}
func (s *Server) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	deny := func() { http.Error(w, "webhook authentication failed", http.StatusUnauthorized) }
	if s.cfg.WebhookKeys == nil {
		http.Error(w, "webhook intake unavailable", http.StatusServiceUnavailable)
		return
	}
	var sub protocol.WebhookSubscription
	err := s.store.Read(r.Context(), func(tx *store.ReadTx) error {
		var e error
		sub, e = store.GetWebhookSubscription(r.Context(), tx.Q(), r.PathValue("id"))
		return e
	})
	if err != nil {
		deny()
		return
	}
	key, err := s.cfg.WebhookKeys.Get(r.Context(), sub.KeyRef)
	if err != nil {
		deny()
		return
	}
	timestamp, delivery := r.Header.Get("X-Tend-Timestamp"), r.Header.Get("X-Tend-Delivery")
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	now := s.cfg.Now()
	if err != nil || seconds < now.Add(-5*time.Minute).Unix() || seconds > now.Add(5*time.Minute).Unix() || len(delivery) == 0 || len(delivery) > 200 {
		deny()
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "event is too large", http.StatusRequestEntityTooLarge)
		return
	}
	signature, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Tend-Signature"), "sha256="))
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s.%s.%d.%s.", sub.ID, timestamp, len(delivery), delivery)
	mac.Write(body)
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		deny()
		return
	}
	if !json.Valid(body) {
		http.Error(w, "event must be JSON", http.StatusBadRequest)
		return
	}
	payload, _ := json.Marshal(app.ReceiveWebhookPayload{SubscriptionID: sub.ID, DeliveryID: delivery, Body: body})
	// Stable sender identity belongs to the system dedup domain, not an owner.
	sum := sha256.Sum256([]byte(sub.ID + "\x00" + delivery))
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	id := []byte("req_0")
	for _, v := range sum[:25] {
		id = append(id, alphabet[v&31])
	}
	env := protocol.Envelope{RequestID: string(id), Operation: "webhooks.receive", Target: protocol.Target{Type: "webhook", ID: sub.ID}, Payload: payload}
	op, err := s.app.Execute(r.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, &env, app.ReceiveWebhook{KeyRef: sub.KeyRef})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if op.Error != nil {
		s.writeError(w, r, op.Error)
		return
	}
	if op.Status != protocol.OpSucceeded {
		http.Error(w, "intake pending", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(op.Result)
}

// This assertion keeps key staging out of the ledger transaction interface.
var _ app.WebhookKeys = (*webhookauth.Files)(nil)
