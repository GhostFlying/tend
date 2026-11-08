package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
)

type ValidationUse struct {
	CategoryID string
	Agent      protocol.AgentIdentity
}

type ContentSource func(context.Context, protocol.ExtensionResource) (io.ReadCloser, error)

// ValidateNative uses disposable homes and the production filesystem view.
// The native probe reads catalogs only; it never sends a model turn or installs
// anything into the shared account. Real credentials are deliberately absent.
func ValidateNative(ctx context.Context, r protocol.ExtensionRelease, source ContentSource, uses []ValidationUse) error {
	user, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	temp, err := os.MkdirTemp("", "tend-extension-validation-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	c := Consumer{ValidationOnly: true, UserHome: user, Config: Configuration{Directory: filepath.Join(temp, "cache"), Runtimes: map[string]Runtime{"validation": {Home: filepath.Join(temp, "state")}}}}
	if err = c.Receive(ctx, r, source); err != nil {
		return err
	}

	selected := map[string]bool{}
	for _, use := range uses {
		selected[use.CategoryID] = true
	}
	for _, category := range r.Catalog.Categories {
		if !selected[category.CategoryID] {
			uses = append(uses, ValidationUse{CategoryID: category.CategoryID, Agent: protocol.AgentIdentity{ExecutionEnvironmentID: "validation", LocalID: "category"}})
		}
	}
	validated := map[string]bool{}
	for _, use := range uses {
		combo, err := Resolve(r, use.CategoryID, use.Agent)
		if err != nil {
			return err
		}
		key, _ := json.Marshal(struct {
			Resources []protocol.ResolvedExtension
			AgentsMD  string
			Bundled   bool
		}{combo.Resources, combo.AgentsMD, combo.BundledSkills})
		fingerprint := Hash(key)
		if validated[fingerprint] {
			continue
		}
		validated[fingerprint] = true
		if err = CheckPackageConflicts(ctx, combo, source); err != nil {
			return err
		}
		if _, _, err = c.Prepare(ctx, combo.Pin, "validation"); err != nil {
			return fmt.Errorf("native extension composition validation failed: %w", err)
		}
	}
	return nil
}
