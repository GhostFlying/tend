// Package protocol defines the wire-level contract shared by the server, the
// runner and the web client: identifiers, the request envelope, principals,
// payload hashing, error codes and domain enums.
package protocol

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Prefix is the type tag that starts every identifier, such as "req".
type Prefix string

// Identifier prefixes.
const (
	PrefixRequest          Prefix = "req"
	PrefixTrace            Prefix = "trc"
	PrefixTask             Prefix = "task"
	PrefixAction           Prefix = "act"
	PrefixRun              Prefix = "run"
	PrefixArtifact         Prefix = "art"
	PrefixFile             Prefix = "file"
	PrefixAcceptance       Prefix = "acc"
	PrefixDecision         Prefix = "dec"
	PrefixApproval         Prefix = "apv"
	PrefixMessage          Prefix = "msg"
	PrefixEvent            Prefix = "evt"
	PrefixOutbox           Prefix = "obx"
	PrefixLease            Prefix = "lse"
	PrefixWorkspace        Prefix = "wsp"
	PrefixWorkspaceCleanup Prefix = "wcl"
	PrefixPacket           Prefix = "pkt"
	// Runner-local identifiers of a TUI worker (D66): the marker in a
	// launch's brief, and the token of its tool bridge.
	PrefixLaunch Prefix = "lch"
	PrefixToken  Prefix = "tok"
)

// bodyLen is the length of the Crockford base32 body: 48 bits of milliseconds
// followed by 80 random bits, the ULID layout.
const bodyLen = 26

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// IDGen creates new identifiers. Business code receives an IDGen instead of
// generating identifiers itself so tests can make them deterministic.
type IDGen interface {
	New(p Prefix) string
}

// ULIDGen generates prefixed ULID-style identifiers. Identifiers created
// within the same millisecond are kept strictly increasing.
type ULIDGen struct {
	now  func() time.Time
	rand io.Reader

	mu       sync.Mutex
	lastMs   uint64
	lastRand [10]byte
}

// NewULIDGen returns a generator that reads time from now and randomness from
// r. A nil r uses crypto/rand.
func NewULIDGen(now func() time.Time, r io.Reader) *ULIDGen {
	if r == nil {
		r = rand.Reader
	}
	return &ULIDGen{now: now, rand: r}
}

// New returns a new identifier with the given prefix.
func (g *ULIDGen) New(p Prefix) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	ms := uint64(g.now().UnixMilli())
	var entropy [10]byte
	if ms == g.lastMs {
		entropy = g.lastRand
		increment(&entropy)
	} else if _, err := io.ReadFull(g.rand, entropy[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a failing custom
		// reader is a programming error.
		panic(fmt.Sprintf("protocol: read entropy: %v", err))
	}
	g.lastMs, g.lastRand = ms, entropy
	return string(p) + "_" + encode(ms, entropy)
}

func increment(b *[10]byte) {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return
		}
	}
}

// encode writes the 128-bit value ms(48) || entropy(80) as 26 base32 digits.
func encode(ms uint64, entropy [10]byte) string {
	var raw [16]byte
	binary.BigEndian.PutUint64(raw[0:8], ms<<16)
	copy(raw[6:], entropy[:])

	hi := binary.BigEndian.Uint64(raw[0:8])
	lo := binary.BigEndian.Uint64(raw[8:16])
	var out [bodyLen]byte
	// 26 digits cover 130 bits; the top two bits are always zero.
	for i := bodyLen - 1; i >= 0; i-- {
		out[i] = crockford[lo&0x1f]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}

// ValidID reports whether id has the given prefix and a well-formed body.
// It checks syntax only; an identifier never grants any permission.
func ValidID(p Prefix, id string) bool {
	body, ok := strings.CutPrefix(id, string(p)+"_")
	if !ok || len(body) != bodyLen {
		return false
	}
	if body[0] > '7' {
		// The first digit carries only three significant bits.
		return false
	}
	for i := 0; i < len(body); i++ {
		if strings.IndexByte(crockford, body[i]) < 0 {
			return false
		}
	}
	return true
}
