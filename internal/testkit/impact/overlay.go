package impact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type overlayFile struct {
	data    []byte
	deleted bool
}

type compilerOverlay struct {
	digest string
	files  map[string]overlayFile
}

// Go permits quotes around an entire GOFLAGS word, without escape processing.
func goFlagWords(flags string) ([]string, error) {
	var result []string
	for {
		flags = strings.TrimLeft(flags, " \t\r\n")
		if flags == "" {
			return result, nil
		}
		if quote := flags[0]; quote == '\'' || quote == '"' {
			end := strings.IndexByte(flags[1:], quote)
			if end < 0 {
				return nil, fmt.Errorf("unterminated quoted GOFLAGS word")
			}
			result = append(result, flags[1:end+1])
			flags = flags[end+2:]
			continue
		}
		end := strings.IndexAny(flags, " \t\r\n")
		if end < 0 {
			return append(result, flags), nil
		}
		result, flags = append(result, flags[:end]), flags[end:]
	}
}

func buildPath(dir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if volume := filepath.VolumeName(dir); volume != "" && strings.HasPrefix(path, string(filepath.Separator)) {
		return filepath.Join(volume, path)
	}
	return filepath.Join(dir, path)
}

func loadCompilerOverlay(dir, flags string) (compilerOverlay, error) {
	var result compilerOverlay
	words, err := goFlagWords(flags)
	if err != nil {
		return result, err
	}
	path := ""
	for _, word := range words {
		if value, ok := strings.CutPrefix(word, "-overlay="); ok {
			path = value
		}
	}
	if path == "" {
		return result, nil
	}
	data, err := os.ReadFile(buildPath(dir, path))
	if err != nil {
		return result, fmt.Errorf("read compiler overlay: %w", err)
	}
	var manifest struct{ Replace map[string]string }
	if err := json.Unmarshal(data, &manifest); err != nil {
		return result, fmt.Errorf("decode compiler overlay: %w", err)
	}
	paths := make([]string, 0, len(manifest.Replace))
	for from := range manifest.Replace {
		paths = append(paths, from)
	}
	slices.Sort(paths)
	identity := []string{hash(data)}
	result.files = map[string]overlayFile{}
	for _, from := range paths {
		if from == "" {
			return result, fmt.Errorf("empty compiler overlay source")
		}
		logical := buildPath(dir, from)
		if _, exists := result.files[logical]; exists {
			return result, fmt.Errorf("duplicate compiler overlay source")
		}
		to := manifest.Replace[from]
		entry := overlayFile{deleted: to == ""}
		if !entry.deleted {
			to = buildPath(dir, to)
			entry.data, err = os.ReadFile(to)
			if err != nil {
				return result, fmt.Errorf("read compiler overlay backing file: %w", err)
			}
		}
		result.files[logical] = entry
		identity = append(identity, logical, to, hash(entry.data))
	}
	b, err := json.Marshal(identity)
	if err != nil {
		return result, err
	}
	result.digest = hash(b)
	return result, nil
}

func (o compilerOverlay) readFile(dir, path string) ([]byte, error) {
	name := buildPath(dir, path)
	if entry, ok := o.files[name]; ok {
		if entry.deleted {
			return nil, os.ErrNotExist
		}
		return entry.data, nil
	}
	return os.ReadFile(name)
}
