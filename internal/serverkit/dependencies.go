package serverkit

import (
	"context"
	"net"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
)

// SessionSpec is the resolved hosted session boundary, shared by front and
// background agents. An injected opener must not launch another default adapter.
type SessionSpec struct {
	Config    MainAgentConfig
	Selection *hostedconfig.Selection
	Cwd       string
	Env       []string
}

// SessionOpener replaces external hosted processes without changing tools.
type SessionOpener func(context.Context, SessionSpec) (harness.Session, error)

// Dependencies leaves production defaults intact when empty. Injected listeners
// transfer serving ownership to Run; WhoIs must describe their peer identity.
type Dependencies struct {
	Listeners   []net.Listener
	WhoIs       netx.WhoIs
	Secure      bool
	OpenSession SessionOpener
	Now         func() time.Time
	IDs         protocol.IDGen
	// Configure runs after static resource sync and before hosted admission.
	Configure  func(context.Context, *app.App, protocol.IDGen) error
	Initialize func(context.Context, *Runtime) error
	Background func(context.Context, *Runtime) error
}

// Runtime exposes assembled services to an isolated initializer or background
// worker. Mutations still pass through App or Tools, never direct store writes.
type Runtime struct {
	App       *app.App
	Store     *store.Store
	IDs       protocol.IDGen
	Tools     *tools.Service
	Listeners []net.Listener
}
