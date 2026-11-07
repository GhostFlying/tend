package serverkit

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/reviewwatch"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/testkit/storefixture"
)

var deliveryObservationFixture storefixture.Template

type deliveryObserverFunc func(context.Context, reviewwatch.Ref) (reviewwatch.Observation, error)

func (f deliveryObserverFunc) Observe(ctx context.Context, ref reviewwatch.Ref) (reviewwatch.Observation, error) {
	return f(ctx, ref)
}

func TestServerDeliveryObserverConfiguration(t *testing.T) {
	for _, c := range []DeliveryObserverConfig{{Providers: []string{"unknown"}}, {Providers: []string{"github"}, GitHubBin: "gh"}, {Timeout: cfgfile.Duration(2 * time.Minute)}} {
		if c.validate() == nil {
			t.Fatal("invalid config accepted", c)
		}
	}
	c := DeliveryObserverConfig{Providers: []string{"github"}, GitHubBin: filepath.Join(t.TempDir(), "missing"), Timeout: cfgfile.Duration(30 * time.Second)}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if deliveryProviderAvailable(c.GitHubBin) {
		t.Fatal("missing executable is available")
	}
	a := app.New(nil, nil, nil, app.WithServerDeliveryObserverStatus(protocol.DeliveryObserverStatus{Enabled: true, Providers: c.Providers, Executables: map[string]bool{"github": false}, TimeoutMillis: 30000}))
	status := a.ServerDeliveryObserverStatus()
	if status == nil || !status.Enabled || status.Executables["github"] {
		t.Fatal(status)
	}
	status.Providers[0] = "modified"
	status.Executables["github"] = true
	if got := a.ServerDeliveryObserverStatus(); got.Providers[0] != "github" || got.Executables["github"] {
		t.Fatal("diagnostic snapshot is mutable", got)
	}
}

func TestServerDeliveryObserverRecordsTimeoutAndMissingProvider(t *testing.T) {
	for _, provider := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing provider", true: "bounded timeout"}[provider], func(t *testing.T) {
			clock := testkit.NewClock()
			s, err := deliveryObservationFixture.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			ids := testkit.NewIDGen(clock, 42)
			a := app.New(s, ids, clock.Now, app.WithServerDeliveryObservations(true))
			item := protocol.WorkItem{WorkItemID: ids.New(protocol.PrefixWorkItem), Revision: 1, Status: "open", CreatedAt: clock.Now().UnixMilli(), UpdatedAt: clock.Now().UnixMilli(), IssuePlan: protocol.IssuePlan{Title: "Delivery", Project: app.DefaultProject, Intent: "Observe", Deliveries: []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/project", Number: 7}}}}
			if err = s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveIssue(t.Context(), item, 0, map[string]any{}) }); err != nil {
				t.Fatal(err)
			}
			calls := 0
			observer := deliveryObserverFunc(func(ctx context.Context, _ reviewwatch.Ref) (reviewwatch.Observation, error) {
				calls++
				<-ctx.Done()
				return reviewwatch.Observation{}, ctx.Err()
			})
			c := DeliveryObserverConfig{Timeout: cfgfile.Duration(time.Millisecond)}
			if provider {
				c.Providers = []string{"github"}
			}
			if err = observeServerDelivery(t.Context(), a, c, observer); err != nil {
				t.Fatal(err)
			}
			if err = s.Read(t.Context(), func(tx *store.ReadTx) error {
				row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), item.WorkItemID, "pr")
				if err != nil {
					return err
				}
				want := "provider_not_configured"
				if provider {
					want = "timeout"
				}
				if row.Observation.ErrorKind != want || row.Observation.RefreshPending || row.Observation.Observer != "server" {
					t.Error(row.Observation)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if (calls == 1) != provider {
				t.Fatal(calls)
			}
		})
	}
}
