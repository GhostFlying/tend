package protocol

import (
	"crypto/sha256"
	"encoding/binary"
)

// NativeCorrelationID is tracing correlation, not an execution or replay grant.
// No request-to-object mapping is persisted.
func NativeCorrelationID(source, key string) string {
	return NativeObjectID(PrefixRequest, source, key)
}

// NativeObjectID lets a native caller supply a stable actual business identity.
func NativeObjectID(prefix Prefix, source, key string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + key))
	var entropy [10]byte
	copy(entropy[:], sum[6:16])
	return string(prefix) + "_" + encode(binary.BigEndian.Uint64(sum[:8])>>16, entropy)
}
