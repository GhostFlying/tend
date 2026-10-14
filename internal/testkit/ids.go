package testkit

import (
	"math/rand/v2"

	"github.com/GhostFlying/tend/internal/protocol"
)

// NewIDGen returns an identifier generator whose output depends only on the
// clock and seed, so test runs produce the same identifiers every time.
func NewIDGen(clock *Clock, seed uint64) protocol.IDGen {
	var key [32]byte
	for i := range 8 {
		key[i] = byte(seed >> (8 * i))
	}
	return protocol.NewULIDGen(clock.Now, rand.NewChaCha8(key))
}
