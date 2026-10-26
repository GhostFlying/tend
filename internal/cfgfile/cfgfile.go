// Package cfgfile reads tend's configuration files: HuJSON, that is JSON
// with comments and trailing commas (D32). Unknown fields are errors, so a
// typo does not silently fall back to a default.
package cfgfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tailscale/hujson"
)

// Load reads the file at path into v.
func Load(path string, v any) error { _, err := LoadDigest(path, v); return err }

// LoadDigest returns the SHA-256 of the exact configuration bytes decoded.
func LoadDigest(path string, v any) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cfgfile: %w", err)
	}
	std, err := hujson.Standardize(raw)
	if err != nil {
		return "", fmt.Errorf("cfgfile: %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(std))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return "", fmt.Errorf("cfgfile: %s: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Path expands a leading ~ to the user's home directory and makes the
// path absolute, resolving a relative one against the working directory,
// so that later directory changes, such as git -C, cannot shift it. An
// empty path stays empty.
func Path(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cfgfile: home directory: %w", err)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("cfgfile: %w", err)
	}
	return abs, nil
}

// Duration is a duration written as a string such as "90s" or "30m".
type Duration time.Duration

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("cfgfile: duration must be a string such as \"30s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("cfgfile: %w", err)
	}
	*d = Duration(v)
	return nil
}
