// Package compatcheck enforces expiry of temporary runner wire adapters.
package compatcheck

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Adapter struct {
	ID       string `json:"id"`
	Protocol int64  `json:"protocol"`
	RemoveIn int64  `json:"remove_in"`
	File     string `json:"file"`
	Test     string `json:"test"`
}
type Registry struct {
	Current  int64     `json:"current"`
	Previous int64     `json:"previous"`
	Adapters []Adapter `json:"adapters"`
}

func Check(root string, current int64) error {
	data, err := os.ReadFile(filepath.Join(root, "internal/protocol/runner_compat.json"))
	if err != nil {
		return err
	}
	var registry Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		return err
	}
	if registry.Current != current || registry.Previous != current-1 {
		return fmt.Errorf("runner compatibility registry window differs from protocol constants")
	}
	declared := map[string]Adapter{}
	for _, a := range registry.Adapters {
		if _, ok := declared[a.ID]; ok {
			return fmt.Errorf("duplicate adapter %s", a.ID)
		}
		if a.Protocol < current-1 || a.Protocol >= current || a.RemoveIn != a.Protocol+2 || current >= a.RemoveIn {
			return fmt.Errorf("expired or invalid adapter %s", a.ID)
		}
		if _, err := os.Stat(filepath.Join(root, a.Test)); err != nil {
			return fmt.Errorf("adapter %s test: %w", a.ID, err)
		}
		declared[a.ID] = a
	}
	seen := map[string]bool{}
	marker := regexp.MustCompile(`(?m)^// tend:compat ([a-z0-9-]+) protocol=([0-9]+) remove=([0-9]+)$`)
	err = filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range marker.FindAllStringSubmatch(string(body), -1) {
			a, ok := declared[m[1]]
			rel, _ := filepath.Rel(root, path)
			p, _ := strconv.ParseInt(m[2], 10, 64)
			remove, _ := strconv.ParseInt(m[3], 10, 64)
			if !ok || seen[m[1]] || filepath.ToSlash(rel) != a.File || p != a.Protocol || remove != a.RemoveIn {
				return fmt.Errorf("undeclared or mismatched compatibility marker %s", m[1])
			}
			seen[m[1]] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	for id := range declared {
		if !seen[id] {
			return fmt.Errorf("missing compatibility marker %s", id)
		}
	}
	return nil
}
