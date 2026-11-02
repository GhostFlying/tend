package app

import (
	"context"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit/storefixture"
	"time"
)

var appFixtureTemplate storefixture.Template

func openAppFixture(ctx context.Context, path string, now func() time.Time) (*store.Store, error) {
	return appFixtureTemplate.Open(ctx, path, now)
}
