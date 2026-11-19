package app

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Run with TEND_TEST_WRITE_PROFILE=1 and -count=1 on the same idle host and
// with this file on both revisions. Results are local, not production targets.
func TestNativeUsageUploadWriteProfile(t *testing.T) {
	if os.Getenv("TEND_TEST_WRITE_PROFILE") != "1" {
		t.Skip("opt-in write profile")
	}
	for _, history := range []int{0, 4096} {
		for _, size := range []int{1, 64, 256} {
			t.Run(fmt.Sprintf("history=%d/size=%d", history, size), func(t *testing.T) {
				h, as := messageRun(t)
				ctx := t.Context()
				if history > 0 {
					if err := h.store.Write(ctx, func(tx *store.Tx) error {
						_, err := tx.Exec(ctx, `WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM seq WHERE i<?)
 INSERT INTO token_usage_sources(source_key,run_id,thread_id,session_id,provider,coverage,updated_at)
 SELECT 'history-'||i,'run-'||i,'thread-'||i,'session-'||i,'traex','caught_up',0 FROM seq`, history)
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				sample := nativeSample(h.clock.Now().UnixMilli())
				registration := protocol.NativeUsageRegistration{RunID: as.RunID, ThreadID: sample.ThreadID, SessionID: sample.SessionID, Provider: sample.Provider}
				if op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerUsageRegister, protocol.Target{Type: "host", ID: "linux-1"}, registration, nil), RegisterNativeUsage{}); op.Error != nil {
					t.Fatal(op)
				}

				observations := make(chan store.WriteObservation, 8)
				store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) {
					if o.Source.Operation == protocol.OpRunnerUsageUpload || o.Source.Operation == "profile.probe" {
						observations <- o
					}
				})(h.store)

				var inserted, replayed []usageProfileMeasurement
				const repeats = 7
				for batch := range repeats {
					samples := make([]protocol.TokenUsageSample, size)
					for i := range samples {
						samples[i] = sample
						id := fmt.Sprintf("%d-%d", batch, i)
						samples[i].RecordID, samples[i].RequestID, samples[i].TurnID = id, id, id
					}
					p := protocol.UsageUpload{RunID: as.RunID, RootThreadID: sample.ThreadID, SessionID: sample.SessionID, Samples: samples, Coverage: "caught_up"}
					inserted = append(inserted, measureNativeUsageProfile(t, h, p, observations))
					replayed = append(replayed, measureNativeUsageProfile(t, h, p, observations))
				}
				for _, result := range []struct {
					mode string
					data []usageProfileMeasurement
				}{{"insert", inserted}, {"replay", replayed}} {
					t.Logf("%s repeats=%d samples=%d median upload_wait=%s callback=%s writer_held=%s probe_wait=%s",
						result.mode, repeats, size, medianUsageProfile(result.data, func(m usageProfileMeasurement) time.Duration { return m.upload.ConnectionWait }),
						medianUsageProfile(result.data, func(m usageProfileMeasurement) time.Duration { return m.upload.Callback }),
						medianUsageProfile(result.data, func(m usageProfileMeasurement) time.Duration {
							return m.upload.Begin + m.upload.Callback + m.upload.Commit
						}),
						medianUsageProfile(result.data, func(m usageProfileMeasurement) time.Duration { return m.probe.ConnectionWait }))
				}
			})
		}
	}
}

type usageProfileMeasurement struct {
	upload, probe store.WriteObservation
}

type profiledUsageUpload struct {
	UploadNativeUsage
	applying chan<- struct{}
}

func (p profiledUsageUpload) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	close(p.applying)
	return p.UploadNativeUsage.Apply(ctx, x, tx)
}

func measureNativeUsageProfile(t *testing.T, h *harness, p protocol.UsageUpload, observations <-chan store.WriteObservation) usageProfileMeasurement {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	applying := make(chan struct{})
	probe := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			probe <- ctx.Err()
		case <-applying:
			probe <- h.store.Write(store.WithWriteOperation(ctx, "profile.probe"), func(*store.Tx) error { return nil })
		}
	}()
	op, err := h.app.Execute(ctx, runner("linux-1"), h.env(protocol.OpRunnerUsageUpload, protocol.Target{Type: "host", ID: "linux-1"}, p, nil), profiledUsageUpload{applying: applying})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	if err := <-probe; err != nil {
		t.Fatal(err)
	}
	var out usageProfileMeasurement
	for range 2 {
		select {
		case o := <-observations:
			if o.Source.Operation == protocol.OpRunnerUsageUpload {
				out.upload = o
			} else {
				out.probe = o
			}
		case <-ctx.Done():
			t.Fatal("missing write observation")
		}
	}
	return out
}

func medianUsageProfile(data []usageProfileMeasurement, read func(usageProfileMeasurement) time.Duration) time.Duration {
	durations := make([]time.Duration, len(data))
	for i, m := range data {
		durations[i] = read(m)
	}
	slices.Sort(durations)
	return durations[len(durations)/2]
}
