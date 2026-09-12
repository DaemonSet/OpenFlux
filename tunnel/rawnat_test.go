package tunnel

import (
	"testing"
	"time"
)

func TestRawNATSeparatesIdenticalClientTuples(t *testing.T) {
	table := newRawNATTable()
	now := time.Unix(1000, 0)
	remote := [4]byte{1, 1, 1, 1}
	one := rawFlowKey{clientIP: [4]byte{10, 10, 10, 2}, clientPort: 40000, remoteIP: remote, remotePort: 443}
	two := rawFlowKey{clientIP: [4]byte{10, 10, 10, 3}, clientPort: 40000, remoteIP: remote, remotePort: 443}

	p1, ok := table.outbound(one, now)
	if !ok {
		t.Fatal("first allocation failed")
	}
	p2, ok := table.outbound(two, now)
	if !ok {
		t.Fatal("second allocation failed")
	}
	if p1 == p2 {
		t.Fatalf("egress collision: %d", p1)
	}

	got1, ok := table.inbound(p1, remote, 443, now)
	if !ok || got1 != one {
		t.Fatalf("first reverse mapping failed: %#v", got1)
	}
	got2, ok := table.inbound(p2, remote, 443, now)
	if !ok || got2 != two {
		t.Fatalf("second reverse mapping failed: %#v", got2)
	}
}
