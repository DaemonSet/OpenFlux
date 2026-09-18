package transport

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	muxMagic       = "OFM2"
	muxClientIDLen = 16
	muxHeaderLen   = len(muxMagic) + muxClientIDLen
	muxClientTTL   = 30 * time.Minute
	muxDNSTTL      = 1 * time.Minute
)

var muxWireClientIP = [4]byte{10, 10, 10, 2}

type muxClientState struct {
	slot     byte
	lastSeen time.Time
}

type muxDNSRoute struct {
	clientID        [muxClientIDLen]byte
	clientRequestID uint32
	lastSeen        time.Time
}

// MultiplexTransport lets multiple clients share one document transport.
//
// Client packets are tagged with a random per-session client ID before they
// enter the compression/encryption layers. On the exit node each client is
// assigned a synthetic 10.10.10.x address. That synthetic address is visible
// only inside the exit's gVisor stack; packets are rewritten back to the
// client's normal 10.10.10.2 address before they leave the multiplexer.
type MultiplexTransport struct {
	Transport

	exitNode bool
	clientID [muxClientIDLen]byte

	mu           sync.Mutex
	clientToSlot map[[muxClientIDLen]byte]muxClientState
	slotToClient map[byte][muxClientIDLen]byte
	nextSlot     byte
	dnsRoutes    map[uint32]muxDNSRoute
	nextDNSID    uint32

	rxUnpackOnce   sync.Once
	rxMatchOnce    sync.Once
	rxMismatchOnce sync.Once
}

func NewMultiplexTransport(inner Transport, exitNode bool) (*MultiplexTransport, error) {
	if inner == nil {
		return nil, errors.New("multiplex inner transport is nil")
	}
	m := &MultiplexTransport{
		Transport:    inner,
		exitNode:     exitNode,
		clientToSlot: make(map[[muxClientIDLen]byte]muxClientState),
		slotToClient: make(map[byte][muxClientIDLen]byte),
		nextSlot:     2,
		dnsRoutes:    make(map[uint32]muxDNSRoute),
	}
	if !exitNode {
		if _, err := rand.Read(m.clientID[:]); err != nil {
			return nil, fmt.Errorf("create multiplex client ID: %w", err)
		}
	}
	return m, nil
}

func (m *MultiplexTransport) ClientID() string {
	if m.exitNode {
		return "exit"
	}
	return hex.EncodeToString(m.clientID[:])
}

func (m *MultiplexTransport) Send(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	if !m.exitNode {
		return m.Transport.Send(muxPack(m.clientID, data))
	}

	if isDNSMuxFrame(data) {
		clientID, control, ok := m.prepareExitDNSOutbound(data, time.Now())
		if !ok {
			return errors.New("multiplex: no client route for DNS control frame")
		}
		return m.Transport.Send(muxPack(clientID, control))
	}

	clientID, packet, ok := m.prepareExitOutbound(data)
	if !ok {
		return errors.New("multiplex: no client route for outbound packet")
	}
	return m.Transport.Send(muxPack(clientID, packet))
}

func (m *MultiplexTransport) Receive(callback func([]byte)) {
	m.Transport.Receive(func(frame []byte) {
		clientID, packet, ok := muxUnpack(frame)
		if !ok {
			return
		}

		m.rxUnpackOnce.Do(func() {
			log.Printf("[RXBOUND] mux unpack OK")
		})

		if !m.exitNode {
			if clientID != m.clientID {
				m.rxMismatchOnce.Do(func() {
					log.Printf("[RXBOUND] mux client-id mismatch")
				})
				return
			}

			m.rxMatchOnce.Do(func() {
				log.Printf("[RXBOUND] mux client-id MATCH")
			})
			callback(packet)
			return
		}

		now := time.Now()
		if isDNSMuxFrame(packet) {
			routed, ok := m.prepareExitDNSInbound(clientID, packet, now)
			if !ok {
				return
			}
			callback(routed)
			return
		}

		slot, ok := m.slotForClient(clientID, now)
		if !ok {
			return
		}
		syntheticIP := [4]byte{10, 10, 10, slot}
		rewritten, ok := rewriteIPv4TCPAddress(packet, true, syntheticIP)
		if !ok {
			return
		}
		callback(rewritten)
	})
}

func muxPack(clientID [muxClientIDLen]byte, packet []byte) []byte {
	out := make([]byte, muxHeaderLen+len(packet))
	copy(out[:len(muxMagic)], muxMagic)
	copy(out[len(muxMagic):muxHeaderLen], clientID[:])
	copy(out[muxHeaderLen:], packet)
	return out
}

func muxUnpack(frame []byte) ([muxClientIDLen]byte, []byte, bool) {
	var clientID [muxClientIDLen]byte
	if len(frame) <= muxHeaderLen || string(frame[:len(muxMagic)]) != muxMagic {
		return clientID, nil, false
	}
	copy(clientID[:], frame[len(muxMagic):muxHeaderLen])
	packet := append([]byte(nil), frame[muxHeaderLen:]...)
	return clientID, packet, true
}

func (m *MultiplexTransport) slotForClient(clientID [muxClientIDLen]byte, now time.Time) (byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.pruneLocked(now)
	if state, ok := m.clientToSlot[clientID]; ok {
		state.lastSeen = now
		m.clientToSlot[clientID] = state
		return state.slot, true
	}

	for attempts := 0; attempts < 253; attempts++ {
		slot := m.nextSlot
		m.nextSlot++
		if m.nextSlot < 2 || m.nextSlot == 255 {
			m.nextSlot = 2
		}
		if _, used := m.slotToClient[slot]; used {
			continue
		}
		m.clientToSlot[clientID] = muxClientState{slot: slot, lastSeen: now}
		m.slotToClient[slot] = clientID
		return slot, true
	}
	return 0, false
}

func (m *MultiplexTransport) prepareExitOutbound(packet []byte) ([muxClientIDLen]byte, []byte, bool) {
	var zero [muxClientIDLen]byte
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return zero, nil, false
	}
	if packet[16] != 10 || packet[17] != 10 || packet[18] != 10 {
		return zero, nil, false
	}
	slot := packet[19]

	m.mu.Lock()
	clientID, ok := m.slotToClient[slot]
	if ok {
		state := m.clientToSlot[clientID]
		state.lastSeen = time.Now()
		m.clientToSlot[clientID] = state
	}
	m.mu.Unlock()
	if !ok {
		return zero, nil, false
	}

	rewritten, ok := rewriteIPv4TCPAddress(packet, false, muxWireClientIP)
	if !ok {
		return zero, nil, false
	}
	return clientID, rewritten, true
}

func isDNSMuxFrame(frame []byte) bool {
	return len(frame) >= dnsMuxHeader && bytes.Equal(frame[:len(dnsMuxMagic)], dnsMuxMagic)
}

// prepareExitDNSInbound gives each client-local DNS request ID a temporary
// exit-wide ID. DNSMuxTransport itself is intentionally unaware of clients;
// the rewritten ID lets simultaneous clients both use request ID 1 safely.
func (m *MultiplexTransport) prepareExitDNSInbound(clientID [muxClientIDLen]byte, frame []byte, now time.Time) ([]byte, bool) {
	if !isDNSMuxFrame(frame) || frame[6] != dnsMuxRequest {
		return nil, false
	}
	clientRequestID := binary.BigEndian.Uint32(frame[7:11])

	m.mu.Lock()
	m.pruneLocked(now)
	globalID, ok := m.allocateDNSIDLocked()
	if ok {
		m.dnsRoutes[globalID] = muxDNSRoute{
			clientID:        clientID,
			clientRequestID: clientRequestID,
			lastSeen:        now,
		}
	}
	m.mu.Unlock()
	if !ok {
		return nil, false
	}

	out := append([]byte(nil), frame...)
	binary.BigEndian.PutUint32(out[7:11], globalID)
	return out, true
}

func (m *MultiplexTransport) prepareExitDNSOutbound(frame []byte, now time.Time) ([muxClientIDLen]byte, []byte, bool) {
	var zero [muxClientIDLen]byte
	if !isDNSMuxFrame(frame) || (frame[6] != dnsMuxResponse && frame[6] != dnsMuxError) {
		return zero, nil, false
	}
	globalID := binary.BigEndian.Uint32(frame[7:11])

	m.mu.Lock()
	m.pruneLocked(now)
	route, ok := m.dnsRoutes[globalID]
	if ok {
		delete(m.dnsRoutes, globalID)
	}
	m.mu.Unlock()
	if !ok {
		return zero, nil, false
	}

	out := append([]byte(nil), frame...)
	binary.BigEndian.PutUint32(out[7:11], route.clientRequestID)
	return route.clientID, out, true
}

func (m *MultiplexTransport) allocateDNSIDLocked() (uint32, bool) {
	for attempts := 0; attempts <= len(m.dnsRoutes); attempts++ {
		m.nextDNSID++
		if m.nextDNSID == 0 {
			m.nextDNSID++
		}
		if _, used := m.dnsRoutes[m.nextDNSID]; !used {
			return m.nextDNSID, true
		}
	}
	return 0, false
}

func (m *MultiplexTransport) pruneLocked(now time.Time) {
	for clientID, state := range m.clientToSlot {
		if now.Sub(state.lastSeen) <= muxClientTTL {
			continue
		}
		delete(m.clientToSlot, clientID)
		delete(m.slotToClient, state.slot)
	}
	for id, route := range m.dnsRoutes {
		if now.Sub(route.lastSeen) <= muxDNSTTL {
			continue
		}
		delete(m.dnsRoutes, id)
	}
}

func rewriteIPv4TCPAddress(packet []byte, source bool, address [4]byte) ([]byte, bool) {
	if len(packet) < 40 || packet[0]>>4 != 4 {
		return nil, false
	}
	ihl := int(packet[0]&0x0f) * 4
	if ihl < 20 || len(packet) < ihl+20 || packet[9] != 6 {
		return nil, false
	}
	totalLen := int(packet[2])<<8 | int(packet[3])
	if totalLen < ihl+20 || totalLen > len(packet) {
		return nil, false
	}
	// We cannot safely update a TCP checksum on non-initial fragments.
	frag := uint16(packet[6])<<8 | uint16(packet[7])
	if frag&0x1fff != 0 {
		return nil, false
	}

	out := append([]byte(nil), packet[:totalLen]...)
	if source {
		copy(out[12:16], address[:])
	} else {
		copy(out[16:20], address[:])
	}

	out[10], out[11] = 0, 0
	ipChecksum := internetChecksum(out[:ihl])
	out[10], out[11] = byte(ipChecksum>>8), byte(ipChecksum)

	tcp := out[ihl:totalLen]
	tcp[16], tcp[17] = 0, 0
	tcpChecksum := tcpIPv4Checksum(out[12:16], out[16:20], tcp)
	tcp[16], tcp[17] = byte(tcpChecksum>>8), byte(tcpChecksum)
	return out, true
}

func internetChecksum(data []byte) uint16 {
	var sum uint32
	for len(data) >= 2 {
		sum += uint32(data[0])<<8 | uint32(data[1])
		data = data[2:]
	}
	if len(data) == 1 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func tcpIPv4Checksum(src, dst, tcp []byte) uint16 {
	var sum uint32
	add := func(data []byte) {
		for len(data) >= 2 {
			sum += uint32(data[0])<<8 | uint32(data[1])
			data = data[2:]
		}
		if len(data) == 1 {
			sum += uint32(data[0]) << 8
		}
	}
	add(src)
	add(dst)
	sum += 6
	sum += uint32(len(tcp))
	add(tcp)
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
