// Package testbudget shares Go subprocess resource limits across check entrypoints.
package testbudget

import (
	"fmt"
	"strconv"
	"strings"
)

// Packages bounds concurrently executing Go packages within one check group.
func Packages(cpus, jobs int) int {
	cpus, jobs = max(1, cpus), max(1, jobs)
	packages := max(1, min(6, cpus/10))
	if jobs > 1 {
		packages = max(packages, min(2, cpus/(2*jobs)))
	}
	return packages
}

// Procs budgets Go execution threads for the largest concurrent process phase.
func Procs(cpus, processes int) int {
	return max(1, min(4, max(1, cpus)/max(1, processes)))
}

// Environment replaces named variables while preserving unrelated caller input.
func Environment(source []string, values map[string]string) []string {
	out := make([]string, 0, len(source)+len(values))
	for _, entry := range source {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			out = append(out, entry)
		}
	}
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

// Value returns the last environment value, as inherited by a subprocess.
func Value(source []string, name string) string {
	var value string
	for _, entry := range source {
		if key, v, ok := strings.Cut(entry, "="); ok && key == name {
			value = v
		}
	}
	return value
}

// PackageLimit recognizes GOFLAGS' whole-word quotes without rewriting flags.
// A quoted compiler argument containing -p= is not a Go package limit.
func PackageLimit(flags string, fallback int) (limit int, explicit bool, err error) {
	limit = fallback
	for flags = strings.TrimSpace(flags); flags != ""; flags = strings.TrimSpace(flags) {
		var word string
		if flags[0] == '\'' || flags[0] == '"' {
			quote := flags[:1]
			var closed bool
			word, flags, closed = strings.Cut(flags[1:], quote)
			if !closed {
				return 0, false, fmt.Errorf("unterminated GOFLAGS quote")
			}
		} else if i := strings.IndexAny(flags, " \t\r\n"); i >= 0 {
			word, flags = flags[:i], flags[i:]
		} else {
			word, flags = flags, ""
		}
		value, found := strings.CutPrefix(word, "-p=")
		if !found {
			continue
		}
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 {
			return 0, false, fmt.Errorf("GOFLAGS package parallelism must be positive")
		}
		explicit = true
	}
	return limit, explicit, nil
}

// GoEnvironment fills only absent Go limits. The parent check's budget and
// explicit caller overrides remain authoritative at independent race entrypoints.
func GoEnvironment(source []string, packages, procs int) ([]string, error) {
	flags := Value(source, "GOFLAGS")
	_, explicit, err := PackageLimit(flags, packages)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	if !explicit {
		values["GOFLAGS"] = strings.TrimSpace(flags + " -p=" + strconv.Itoa(packages))
	}
	if strings.TrimSpace(Value(source, "GOMAXPROCS")) == "" {
		values["GOMAXPROCS"] = strconv.Itoa(procs)
	}
	return Environment(source, values), nil
}
