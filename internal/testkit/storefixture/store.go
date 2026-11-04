// Package storefixture creates independent, current-schema test databases.
// Migration and reopen tests should continue to use store.Open directly.
package storefixture

import (
	"context"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit/dbfixture"
)

// Template lazily migrates one empty database and shares only immutable bytes.
// The zero value is ready for use. A Template must not be copied after use.
// Migration timestamps come from the first caller; each opened store uses its
// own clock, connections, database files and original-file directory.
type Template struct {
	files dbfixture.Template
}

// Open copies the template to a new path, then opens it through store.Open.
// Existing databases are never overwritten. Template initialization errors are
// retained so a broken migration cannot be hidden by a subsequent fixture.
func (f *Template) Open(ctx context.Context, path string, now func() time.Time) (*store.Store, error) {
	if err := f.files.Copy(ctx, path, func(ctx context.Context, path string) error {
		db, err := store.Open(ctx, path, now)
		if err != nil {
			return fmt.Errorf("migrate store fixture template: %w", err)
		}
		return db.Close()
	}); err != nil {
		return nil, err
	}
	return store.Open(ctx, path, now)
}
