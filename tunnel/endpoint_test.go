package tunnel

import (
	"errors"
	"testing"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func tunnelTestPackets(t *testing.T, payloads ...[]byte) stack.PacketBufferList {
	t.Helper()

	var packets stack.PacketBufferList
	for _, payload := range payloads {
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(append([]byte(nil), payload...)),
		})
		packets.PushBack(pkt)
		t.Cleanup(pkt.DecRef)
	}
	return packets
}

func TestTunnelLinkEndpointWritePacketsSuccess(t *testing.T) {
	ep := NewTunnelLinkEndpoint()

	var sent [][]byte
	ep.onOutgoingPacket = func(data []byte) error {
		sent = append(sent, append([]byte(nil), data...))
		return nil
	}

	packets := tunnelTestPackets(
		t,
		[]byte("one"),
		[]byte("two"),
	)

	n, err := ep.WritePackets(packets)
	if err != nil {
		t.Fatalf("WritePackets error=%T; want nil", err)
	}
	if n != 2 {
		t.Fatalf("WritePackets n=%d want=2", n)
	}
	if got := ep.packetOut.Load(); got != 2 {
		t.Fatalf("packetOut=%d want=2", got)
	}
	if len(sent) != 2 || string(sent[0]) != "one" || string(sent[1]) != "two" {
		t.Fatalf("sent=%q want=[one two]", sent)
	}
}

func TestTunnelLinkEndpointPropagatesTransportBackpressure(t *testing.T) {
	ep := NewTunnelLinkEndpoint()

	calls := 0
	ep.onOutgoingPacket = func(data []byte) error {
		calls++
		if calls == 2 {
			return errors.New("transport unavailable")
		}
		return nil
	}

	packets := tunnelTestPackets(
		t,
		[]byte("accepted"),
		[]byte("rejected"),
		[]byte("must-not-be-attempted"),
	)

	n, err := ep.WritePackets(packets)
	if n != 1 {
		t.Fatalf("WritePackets n=%d want=1", n)
	}
	if _, ok := err.(*tcpip.ErrNoBufferSpace); !ok {
		t.Fatalf("WritePackets error=%T want *tcpip.ErrNoBufferSpace", err)
	}
	if calls != 2 {
		t.Fatalf("transport calls=%d want=2", calls)
	}
	if got := ep.packetOut.Load(); got != 1 {
		t.Fatalf("packetOut=%d want=1 successful packet", got)
	}
}
