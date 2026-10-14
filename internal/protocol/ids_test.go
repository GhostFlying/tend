package protocol

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func fixedNow(ms int64) func() time.Time {
	return func() time.Time { return time.UnixMilli(ms) }
}

func TestULIDGenFormat(t *testing.T) {
	g := NewULIDGen(fixedNow(1_791_993_954_202), nil)
	id := g.New(PrefixTask)
	if !strings.HasPrefix(id, "task_") || len(id) != len("task_")+bodyLen {
		t.Fatalf("New(PrefixTask) = %q, want task_ followed by %d digits", id, bodyLen)
	}
	if !ValidID(PrefixTask, id) {
		t.Errorf("ValidID(PrefixTask, %q) = false, want true", id)
	}
	if ValidID(PrefixRequest, id) {
		t.Errorf("ValidID(PrefixRequest, %q) = true, want false", id)
	}
}

func TestULIDGenEncodesTimeFirst(t *testing.T) {
	zero := bytes.NewReader(make([]byte, 64))
	got := NewULIDGen(fixedNow(1), zero).New(PrefixEvent)
	if want := "evt_00000000010000000000000000"; got != want {
		t.Errorf("New at 1ms with zero entropy = %q, want %q", got, want)
	}
}

func TestULIDGenMonotonicWithinMillisecond(t *testing.T) {
	g := NewULIDGen(fixedNow(42), nil)
	prev := g.New(PrefixRequest)
	for range 1000 {
		next := g.New(PrefixRequest)
		if next <= prev {
			t.Fatalf("identifiers not increasing: %q then %q", prev, next)
		}
		prev = next
	}
}

func TestULIDGenOrdersByTime(t *testing.T) {
	ms := int64(1000)
	g := NewULIDGen(func() time.Time { return time.UnixMilli(ms) }, nil)
	a := g.New(PrefixRun)
	ms++
	b := g.New(PrefixRun)
	if b <= a {
		t.Errorf("later identifier %q does not sort after %q", b, a)
	}
}

func TestValidID(t *testing.T) {
	good := NewULIDGen(fixedNow(7), nil).New(PrefixRequest)
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"generated", good, true},
		{"empty", "", false},
		{"prefix only", "req_", false},
		{"wrong prefix", "task_" + good[4:], false},
		{"short body", good[:len(good)-1], false},
		{"long body", good + "0", false},
		{"lowercase", strings.ToLower(good), false},
		{"excluded letter", good[:len(good)-1] + "U", false},
		{"overflowing first digit", "req_8" + good[5:], false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidID(PrefixRequest, tt.id); got != tt.want {
				t.Errorf("ValidID(PrefixRequest, %q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}
