package nativeusage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type historyHeader struct {
	path string
	meta metadata
}
type historyIndex struct {
	at      int64
	headers []historyHeader
}

// Discovery shares metadata scans across sources in the same native store.
// It retains no conversation content and never supplies usage facts itself.
type Discovery struct {
	mu     sync.Mutex
	stores map[string]historyIndex
}

func NewDiscovery() *Discovery { return &Discovery{stores: map[string]historyIndex{}} }

type DiscoveryCycle struct {
	index     *Discovery
	now       int64
	refreshed map[string]bool
}

// Cycle coalesces forced final scans from multiple stopped writers in a sweep.
func (d *Discovery) Cycle(now int64) *DiscoveryCycle {
	return &DiscoveryCycle{index: d, now: now, refreshed: map[string]bool{}}
}

func (c *DiscoveryCycle) entries(ctx context.Context, key string, roots []string, force bool) ([]historyHeader, error) {
	c.index.mu.Lock()
	defer c.index.mu.Unlock()
	if cached, ok := c.index.stores[key]; ok && c.now-cached.at < 30000 && !(force && !c.refreshed[key]) {
		return cached.headers, nil
	}
	var headers []historyHeader
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			m, _, err := header(f)
			_ = f.Close()
			if err == nil {
				headers = append(headers, historyHeader{path: path, meta: m})
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("native usage: index registered store: %w", err)
		}
	}
	c.index.stores[key] = historyIndex{at: c.now, headers: headers}
	c.refreshed[key] = true
	return headers, nil
}
