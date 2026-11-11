package protocol

import (
	"fmt"
	"testing"
)

func TestNativeBusinessIdentityIsStableAndValid(t *testing.T) {
	for _, prefix := range []Prefix{PrefixTask, PrefixWorkItem, PrefixDecision, Prefix("attn"), Prefix("consultation:conversation")} {
		for i := 0; i < 128; i++ {
			key := fmt.Sprintf("thread/turn/item/%d", i)
			got := NativeObjectID(prefix, "native-caller", key)
			if !ValidID(prefix, got) || got != NativeObjectID(prefix, "native-caller", key) {
				t.Fatalf("invalid or unstable business identity %q", got)
			}
			if got == NativeObjectID(prefix, "another-caller", key) {
				t.Fatal("native source identity collided")
			}
		}
	}
}
