package serverkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
)

type feishuRouteFixture struct{ calls int }

func (f *feishuRouteFixture) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.calls++
	w.WriteHeader(http.StatusUnauthorized)
}
func (*feishuRouteFixture) Deliver(context.Context, app.Delivery) app.DeliveryResult {
	return app.DeliveryResult{Outcome: app.Unknown}
}
func (*feishuRouteFixture) Reconcile(context.Context, app.Delivery) app.DeliveryResult {
	return app.DeliveryResult{Outcome: app.Unknown}
}
func (*feishuRouteFixture) Run(context.Context) {}
func TestFeishuCallbackRouteDoesNotReplaceOwnerAuthentication(t *testing.T) {
	var ownerCalls int
	owner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { ownerCalls++; w.WriteHeader(http.StatusForbidden) })
	transport := &feishuRouteFixture{}
	for _, enabled := range []bool{false, true} {
		var f feishuTransport
		if enabled {
			f = transport
		}
		handler := withFeishuCallback(owner, f)
		for _, path := range []string{"/hooks/feishu", "/api/v1/messaging/feishu"} {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
			if enabled && path == "/hooks/feishu" {
				if w.Code != 401 {
					t.Fatal(w.Code)
				}
			} else if w.Code != 403 {
				t.Fatal(w.Code)
			}
		}
	}
	if transport.calls != 1 || ownerCalls != 3 {
		t.Fatal(transport.calls, ownerCalls)
	}
}
