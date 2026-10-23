package journal

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestProcessGenerationSurvivesJournalRollbackAndConcurrentStarts(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(ctx, path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := j.NextProcessGeneration(ctx)
	if err != nil || first != 1 {
		t.Fatalf("first=%d %v", first, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	j, err = Open(ctx, path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := j.NextProcessGeneration(ctx)
	if err != nil || second != 2 {
		t.Fatalf("second=%d %v", second, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, backup, 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var values []int64
	for range 8 {
		wg.Go(func() {
			// Each contender represents an independent process opening the same path.
			j := &Journal{path: path}
			v, err := j.NextProcessGeneration(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			values = append(values, v)
			mu.Unlock()
		})
	}
	wg.Wait()
	slices.Sort(values)
	if !slices.Equal(values, []int64{3, 4, 5, 6, 7, 8, 9, 10}) {
		t.Fatalf("reused generation after rollback/concurrency: %v", values)
	}
}
