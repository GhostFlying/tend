package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func originalVerificationFixture(t *testing.T) (*Store, protocol.OriginalVerification) {
	t.Helper()
	s, _ := openTest(t)
	for _, letter := range []string{"a", "b"} {
		raw := `{"text":"` + strings.Repeat(letter, 2048) + `"}`
		ctx, err := s.StageOriginals(t.Context(), "pool-verification", []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Write(ctx, func(tx *Tx) error { _, err := tx.RetainJSON(ctx, raw); return err }); err != nil {
			t.Fatal(err)
		}
	}
	want, err := s.VerifyOriginals(t.Context(), "value:", 1)
	if err != nil || want.Checked != 1 || !want.More || want.Next == "" || want.Bytes == 0 {
		t.Fatalf("fixture: %+v %v", want, err)
	}
	return s, want
}

func holdVerificationReaders(t *testing.T, s *Store, count int) func() {
	t.Helper()
	var release []func() error
	var once sync.Once
	done := func() {
		once.Do(func() {
			for _, close := range release {
				if err := close(); err != nil {
					t.Error(err)
				}
			}
		})
	}
	t.Cleanup(done)
	for range count {
		conn, err := s.read.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		release = append(release, conn.Close)
	}

	return done
}

func TestOriginalVerificationConcurrentWithOneAvailableReader(t *testing.T) {
	s, want := originalVerificationFixture(t)
	// Keep the real pool cap. Ordinary long-lived readers leave exactly one
	// connection for all verification pages and the unrelated business read.
	release := holdVerificationReaders(t, s, readConnections-1)
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, readConnections+1)
	var wg sync.WaitGroup
	for range readConnections {
		wg.Go(func() {
			<-start
			got, err := s.VerifyOriginals(ctx, "value:", 1)
			if err == nil && got != want {
				err = fmt.Errorf("verification changed: got=%+v want=%+v", got, want)
			}
			results <- err
		})
	}
	wg.Go(func() {
		<-start
		results <- s.Read(ctx, func(tx *ReadTx) error { var n int; return tx.Q().QueryRowContext(ctx, `SELECT 1`).Scan(&n) })
	})
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Error(err)
		}
	}
	if got := s.PoolStats().Reader; got.OpenConnections > readConnections {
		t.Fatalf("pool cap changed: %+v", got)
	}
}

func TestOriginalVerificationCancellationReleasesReadWait(t *testing.T) {
	s, want := originalVerificationFixture(t)
	release := holdVerificationReaders(t, s, readConnections)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	before := s.PoolStats().Reader.WaitCount
	go func() { _, err := s.VerifyOriginals(ctx, "value:", 1); done <- err }()
	watchdog := time.NewTimer(10 * time.Second)
	defer watchdog.Stop()
	for s.PoolStats().Reader.WaitCount == before {
		select {
		case err := <-done:
			t.Fatalf("verification did not wait on full pool: %v", err)
		case <-watchdog.C:
			t.Fatal("verification did not reach pool wait")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
	got, err := s.VerifyOriginals(t.Context(), "value:", 1)
	if err != nil || got != want {
		t.Fatalf("verification did not recover: %+v %v", got, err)
	}
}
