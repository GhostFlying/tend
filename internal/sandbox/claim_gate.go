package sandbox

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

// seedClaimGate holds only the sandbox transport while bulk owner fixtures are
// defined. Story turns and all post-startup claims still use the real server.
type seedClaimGate struct {
	base  http.RoundTripper
	token chan struct{}
}

func (g *seedClaimGate) acquire(ctx context.Context) (func(), error) {
	select {
	case g.token <- struct{}{}:
		return func() { <-g.token }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (g *seedClaimGate) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.GetBody != nil {
		body, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		var env protocol.Envelope
		err = json.NewDecoder(body).Decode(&env)
		_ = body.Close()
		if err == nil && env.Operation == protocol.OpRunnerClaim {
			release, err := g.acquire(r.Context())
			if err != nil {
				return nil, err
			}
			defer release()
		}
	}
	return g.base.RoundTrip(r)
}
