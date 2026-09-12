package transport

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestMuxPackRoundTrip(t *testing.T) {
	var id [muxClientIDLen]byte
	copy(id[:], []byte("client-000000001"))
	packet := testIPv4TCPPacket([4]byte{10, 10, 10, 2}, [4]byte{1, 1, 1, 1}, 40000, 443)
	frame := muxPack(id, packet)
	gotID, gotPacket, ok := muxUnpack(frame)
	if !ok {
		t.Fatal("mux frame rejected")
	}
	if gotID != id || !bytes.Equal(gotPacket, packet) {
		t.Fatal("mux round trip differs")
	}
}

func TestExitClientAddressRoundTrip(t *testing.T) {
	m, err := NewMultiplexTransport(&multiplexTestTransport{}, true)
	if err != nil {
		t.Fatal(err)
	}
	var id [muxClientIDLen]byte
	copy(id[:], []byte("client-000000001"))
	slot, ok := m.slotForClient(id, time.Now())
	if !ok {
		t.Fatal("no client slot")
	}

	in := testIPv4TCPPacket([4]byte{10, 10, 10, 2}, [4]byte{1, 1, 1, 1}, 40000, 443)
	synthetic, ok := rewriteIPv4TCPAddress(in, true, [4]byte{10, 10, 10, slot})
	if !ok || synthetic[15] != slot {
		t.Fatal("source was not rewritten to synthetic client IP")
	}

	response := testIPv4TCPPacket([4]byte{1, 1, 1, 1}, [4]byte{10, 10, 10, slot}, 443, 40000)
	gotID, wire, ok := m.prepareExitOutbound(response)
	if !ok || gotID != id {
		t.Fatal("exit could not route response to client")
	}
	if !bytes.Equal(wire[16:20], muxWireClientIP[:]) {
		t.Fatalf("wire destination=%v", wire[16:20])
	}
	if internetChecksum(wire[:20]) != 0 {
		t.Fatal("IPv4 checksum invalid after rewrite")
	}
	if tcpIPv4Checksum(wire[12:16], wire[16:20], wire[20:]) != 0 {
		t.Fatal("TCP checksum invalid after rewrite")
	}
}

type multiplexTestTransport struct{}

func (*multiplexTestTransport) Start() error          { return nil }
func (*multiplexTestTransport) Stop() error           { return nil }
func (*multiplexTestTransport) Send([]byte) error     { return nil }
func (*multiplexTestTransport) Receive(func([]byte))  {}
func (*multiplexTestTransport) IsConnected() bool     { return true }
func (*multiplexTestTransport) Stats() TransportStats { return TransportStats{} }

func testIPv4TCPPacket(src, dst [4]byte, srcPort, dstPort uint16) []byte {
	pkt := make([]byte, 40)
	pkt[0] = 0x45
	pkt[2], pkt[3] = 0, 40
	pkt[8] = 64
	pkt[9] = 6
	copy(pkt[12:16], src[:])
	copy(pkt[16:20], dst[:])
	pkt[20], pkt[21] = byte(srcPort>>8), byte(srcPort)
	pkt[22], pkt[23] = byte(dstPort>>8), byte(dstPort)
	pkt[32] = 0x50
	pkt[33] = 0x10
	pkt[34], pkt[35] = 0xff, 0xff
	pkt[10], pkt[11] = 0, 0
	ipcs := internetChecksum(pkt[:20])
	pkt[10], pkt[11] = byte(ipcs>>8), byte(ipcs)
	tcpcs := tcpIPv4Checksum(pkt[12:16], pkt[16:20], pkt[20:])
	pkt[36], pkt[37] = byte(tcpcs>>8), byte(tcpcs)
	return pkt
}

type captureTransport struct {
	recv func([]byte)
	sent [][]byte
}

func (c *captureTransport) Start() error { return nil }
func (c *captureTransport) Stop() error  { return nil }
func (c *captureTransport) Send(data []byte) error {
	c.sent = append(c.sent, append([]byte(nil), data...))
	return nil
}
func (c *captureTransport) Receive(cb func([]byte)) { c.recv = cb }
func (c *captureTransport) IsConnected() bool       { return true }
func (c *captureTransport) Stats() TransportStats   { return TransportStats{} }
func (c *captureTransport) inject(data []byte) {
	if c.recv != nil {
		c.recv(data)
	}
}

func testDNSControl(kind byte, id uint32, payload []byte) []byte {
	frame := make([]byte, dnsMuxHeader+len(payload))
	copy(frame, dnsMuxMagic)
	frame[6] = kind
	binary.BigEndian.PutUint32(frame[7:11], id)
	copy(frame[11:], payload)
	return frame
}

func TestMuxDNSControlRoutesTwoClientsWithSameLocalID(t *testing.T) {
	inner := &captureTransport{}
	exit, err := NewMultiplexTransport(inner, true)
	if err != nil {
		t.Fatal(err)
	}

	var id1, id2 [muxClientIDLen]byte
	copy(id1[:], []byte("client-000000001"))
	copy(id2[:], []byte("client-000000002"))
	const localID = uint32(1)

	var delivered [][]byte
	exit.Receive(func(data []byte) {
		delivered = append(delivered, append([]byte(nil), data...))
	})

	inner.inject(muxPack(id1, testDNSControl(dnsMuxRequest, localID, []byte("one"))))
	inner.inject(muxPack(id2, testDNSControl(dnsMuxRequest, localID, []byte("two"))))
	if len(delivered) != 2 {
		t.Fatalf("delivered=%d want 2", len(delivered))
	}

	g1 := binary.BigEndian.Uint32(delivered[0][7:11])
	g2 := binary.BigEndian.Uint32(delivered[1][7:11])
	if g1 == 0 || g2 == 0 || g1 == g2 {
		t.Fatalf("global IDs not isolated: g1=%d g2=%d", g1, g2)
	}

	if err := exit.Send(testDNSControl(dnsMuxResponse, g2, []byte("resp-two"))); err != nil {
		t.Fatal(err)
	}
	if err := exit.Send(testDNSControl(dnsMuxResponse, g1, []byte("resp-one"))); err != nil {
		t.Fatal(err)
	}
	if len(inner.sent) != 2 {
		t.Fatalf("sent=%d want 2", len(inner.sent))
	}

	gotID2, resp2, ok := muxUnpack(inner.sent[0])
	if !ok || gotID2 != id2 {
		t.Fatalf("first response routed to wrong client: %x", gotID2)
	}
	if binary.BigEndian.Uint32(resp2[7:11]) != localID || string(resp2[11:]) != "resp-two" {
		t.Fatalf("client2 response not restored: id=%d payload=%q", binary.BigEndian.Uint32(resp2[7:11]), resp2[11:])
	}

	gotID1, resp1, ok := muxUnpack(inner.sent[1])
	if !ok || gotID1 != id1 {
		t.Fatalf("second response routed to wrong client: %x", gotID1)
	}
	if binary.BigEndian.Uint32(resp1[7:11]) != localID || string(resp1[11:]) != "resp-one" {
		t.Fatalf("client1 response not restored: id=%d payload=%q", binary.BigEndian.Uint32(resp1[7:11]), resp1[11:])
	}
}

func TestMuxTwoAndroidLikeClientsGetDistinctSyntheticIPs(t *testing.T) {
	inner := &captureTransport{}
	exit, err := NewMultiplexTransport(inner, true)
	if err != nil {
		t.Fatal(err)
	}

	var id1, id2 [muxClientIDLen]byte
	copy(id1[:], []byte("client-000000001"))
	copy(id2[:], []byte("client-000000002"))
	packet := testIPv4TCPPacket([4]byte{10, 10, 10, 2}, [4]byte{1, 1, 1, 1}, 40000, 443)

	var delivered [][]byte
	exit.Receive(func(data []byte) { delivered = append(delivered, append([]byte(nil), data...)) })
	inner.inject(muxPack(id1, packet))
	inner.inject(muxPack(id2, packet))
	if len(delivered) != 2 {
		t.Fatalf("delivered=%d want 2", len(delivered))
	}
	if bytes.Equal(delivered[0][12:16], delivered[1][12:16]) {
		t.Fatalf("clients share synthetic source IP: %v", delivered[0][12:16])
	}
	if !bytes.Equal(delivered[0][12:15], []byte{10, 10, 10}) || !bytes.Equal(delivered[1][12:15], []byte{10, 10, 10}) {
		t.Fatalf("unexpected synthetic subnet: %v %v", delivered[0][12:16], delivered[1][12:16])
	}
}
