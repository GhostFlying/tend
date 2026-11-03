package protocol

import "github.com/GhostFlying/tend/internal/release"

// ServerCapabilities is also used by the offline typed CLI diagnostic.
type ServerCapabilities = release.ServerCapabilities

const ServerCapabilityManagerPark = release.ServerCapabilityManagerPark
const LocalServerCapabilities = "local EXEC server --capabilities"
