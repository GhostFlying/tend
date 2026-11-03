//go:build linux

package impact

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type processSample struct {
	parent int
	start  string
	ticks  float64
	kind   string
}
type resourceClass struct {
	Processes  int
	CPUSeconds float64
}
type resourcePoint struct {
	Time    string
	Load    []float64
	Classes map[string]resourceClass
}

// Classify only allowlisted labels. Raw command lines, paths and environment
// values are never written into the resource report.
func resourceKind(args []string) string {
	if len(args) == 0 {
		return ""
	}
	base := filepath.Base(args[0])
	switch base {
	case "compile", "asm", "cgo":
		return "go_compile"
	case "link":
		return "go_link"
	case "go":
		return "go_driver"
	}
	if strings.HasSuffix(base, ".test") {
		return "go_test"
	}
	if base != "node" && !strings.Contains(base, "pnpm") && base != "vite" {
		return ""
	}
	for _, arg := range args {
		switch filepath.Base(arg) {
		case "vitest", "vitest.mjs", "vitest.js":
			return "web_test"
		case "tsc", "tsc.js":
			return "web_typecheck"
		case "eslint.js":
			return "web_lint"
		case "prettier.cjs", "prettier.mjs":
			return "web_format"
		case "style-check.ts":
			return "web_style"
		}
	}
	for _, arg := range args {
		switch arg {
		case "install":
			return "web_install"
		case "typecheck":
			return "web_typecheck"
		case "lint":
			return "web_lint"
		case "format:check":
			return "web_format"
		case "style:check":
			return "web_style"
		case "build":
			return "web_build"
		}
	}
	return ""
}

func startResourceMonitor(ctx context.Context, path string) (func(), error) {
	rate, err := output(ctx, "", "getconf", "CLK_TCK")
	if err != nil {
		return nil, err
	}
	hz, err := strconv.ParseFloat(strings.TrimSpace(string(rate)), 64)
	if err != nil || hz <= 0 {
		return nil, fmt.Errorf("invalid process clock frequency")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	stop, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		defer f.Close()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		previous := map[string]float64{}
		encoder := json.NewEncoder(f)
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
			}
			entries, err := os.ReadDir("/proc")
			if err != nil {
				return
			}
			processes := map[int]processSample{}
			for _, entry := range entries {
				pid, err := strconv.Atoi(entry.Name())
				if err != nil {
					continue
				}
				b, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
				if err != nil {
					continue
				}
				end := strings.LastIndex(string(b), ")")
				if end < 0 {
					continue
				}
				fields := strings.Fields(string(b)[end+1:])
				if len(fields) < 20 {
					continue
				}
				parent, err := strconv.Atoi(fields[1])
				if err != nil {
					continue
				}
				user, err := strconv.ParseFloat(fields[11], 64)
				if err != nil {
					continue
				}
				system, err := strconv.ParseFloat(fields[12], 64)
				if err != nil {
					continue
				}
				processes[pid] = processSample{parent: parent, start: fields[19], ticks: user + system}
			}
			owned := map[int]bool{os.Getpid(): true}
			for changed := true; changed; {
				changed = false
				for pid, p := range processes {
					if owned[p.parent] && !owned[pid] {
						owned[pid] = true
						changed = true
					}
				}
			}
			for pid := range owned {
				p, ok := processes[pid]
				if !ok {
					continue
				}
				b, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
				p.kind = resourceKind(strings.Split(string(b), "\x00"))
				processes[pid] = p
			}
			for changed := true; changed; {
				changed = false
				for pid := range owned {
					p, ok := processes[pid]
					if ok && p.kind == "" {
						if kind := processes[p.parent].kind; kind != "" {
							p.kind = kind
							processes[pid] = p
							changed = true
						}
					}
				}
			}
			point := resourcePoint{Time: time.Now().UTC().Format(time.RFC3339Nano), Classes: map[string]resourceClass{}}
			if load, err := os.ReadFile("/proc/loadavg"); err == nil {
				fields := strings.Fields(string(load))
				for _, v := range fields[:min(3, len(fields))] {
					n, _ := strconv.ParseFloat(v, 64)
					point.Load = append(point.Load, n)
				}
			}
			next := map[string]float64{}
			for pid := range owned {
				p, ok := processes[pid]
				if !ok {
					continue
				}
				key := strconv.Itoa(pid) + ":" + p.start
				next[key] = p.ticks
				kind := p.kind
				if kind == "" {
					kind = "other"
				}
				value := point.Classes[kind]
				value.Processes++
				value.CPUSeconds += max(0, p.ticks-previous[key]) / hz
				point.Classes[kind] = value
			}
			previous = next
			if err := encoder.Encode(point); err != nil {
				return
			}
		}
	}()
	return func() { once.Do(func() { close(stop) }); <-done }, nil
}
