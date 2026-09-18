package tunnel

import (
	"context"
	"errors"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

func TestDialTCPTimeoutCancelsBlackholedConnect(t *testing.T) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})

	ep := NewTunnelLinkEndpoint()
	ep.onOutgoingPacket = func([]byte) error {
		// Accept and drop every packet. This simulates a lower path that is
		// writable but never returns a SYN-ACK.
		return nil
	}

	const nic = tcpip.NICID(1)
	if err := s.CreateNIC(nic, ep); err != nil {
		t.Fatalf("CreateNIC: %v", err)
	}

	tunnel := &TCPTunnel{
		gvisorStack: s,
		tunnelEP:    ep,
		dialTimeout: 50 * time.Millisecond,
	}
	tunnel.setupClient(nic)

	start := time.Now()
	conn, err := tunnel.DialTCP("192.0.2.1:80")
	elapsed := time.Since(start)

	if conn != nil {
		conn.Close()
		t.Fatal("DialTCP unexpectedly returned a connection")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DialTCP error=%v; want context deadline exceeded", err)
	}
	if elapsed > time.Second {
		t.Fatalf("DialTCP took %v; expected bounded cancellation", elapsed)
	}
}

func TestResolveTCPAddrContextIPv4Literal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	addr, err := resolveTCPAddrContext(ctx, "203.0.113.7:443")
	if err != nil {
		t.Fatalf("resolveTCPAddrContext: %v", err)
	}
	if got := addr.IP.String(); got != "203.0.113.7" {
		t.Fatalf("IP=%s want=203.0.113.7", got)
	}
	if addr.Port != 443 {
		t.Fatalf("port=%d want=443", addr.Port)
	}
}

func TestResolveTCPAddrContextRejectsIPv6(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := resolveTCPAddrContext(ctx, "[2001:db8::1]:443")
	if err == nil {
		t.Fatal("IPv6 address unexpectedly accepted")
	}
}
