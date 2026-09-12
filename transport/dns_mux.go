package transport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var dnsMuxMagic = []byte{0xf0, 'O', 'F', 'D', 'N', 'S'}

const (
	dnsMuxRequest  byte = 1
	dnsMuxResponse byte = 2
	dnsMuxError    byte = 3
	dnsMuxHeader        = 11
)

type dnsMuxResult struct {
	data []byte
	err  error
}

// DNSMuxTransport leaves normal tunnel packets untouched.
// Only frames beginning with dnsMuxMagic are consumed as DNS RPC messages.
type DNSMuxTransport struct {
	Transport

	exitNode bool

	receiverMu sync.RWMutex
	receiver   func([]byte)

	pendingMu sync.Mutex
	pending   map[uint32]chan dnsMuxResult
	nextID    atomic.Uint32
}

func NewDNSMuxTransport(inner Transport, exitNode bool) *DNSMuxTransport {
	d := &DNSMuxTransport{
		Transport: inner,
		exitNode:  exitNode,
		pending:   make(map[uint32]chan dnsMuxResult),
	}

	inner.Receive(d.handleReceive)
	return d
}

func (d *DNSMuxTransport) Receive(callback func([]byte)) {
	d.receiverMu.Lock()
	d.receiver = callback
	d.receiverMu.Unlock()
}

func (d *DNSMuxTransport) Stop() error {
	d.pendingMu.Lock()
	for id, ch := range d.pending {
		select {
		case ch <- dnsMuxResult{err: errors.New("transport stopped")}:
		default:
		}
		delete(d.pending, id)
	}
	d.pendingMu.Unlock()

	return d.Transport.Stop()
}

func (d *DNSMuxTransport) QueryDNS(query []byte, server string) ([]byte, error) {
	if d.exitNode {
		return nil, errors.New("DNS query called on exit node")
	}
	if len(query) < 12 || len(query) > 65535 {
		return nil, fmt.Errorf("invalid DNS query size: %d", len(query))
	}

	ip := net.ParseIP(server)
	if ip == nil {
		return nil, fmt.Errorf("invalid DNS server: %s", server)
	}
	if ip4 := ip.To4(); ip4 != nil {
		server = ip4.String()
	} else {
		server = ip.String()
	}
	if len(server) > 255 {
		return nil, errors.New("DNS server address too long")
	}

	id := d.nextID.Add(1)
	if id == 0 {
		id = d.nextID.Add(1)
	}

	ch := make(chan dnsMuxResult, 1)

	d.pendingMu.Lock()
	d.pending[id] = ch
	d.pendingMu.Unlock()

	defer func() {
		d.pendingMu.Lock()
		delete(d.pending, id)
		d.pendingMu.Unlock()
	}()

	payload := make([]byte, 1+len(server)+len(query))
	payload[0] = byte(len(server))
	copy(payload[1:], server)
	copy(payload[1+len(server):], query)

	if err := d.sendControl(dnsMuxRequest, id, payload); err != nil {
		return nil, err
	}

	select {
	case result := <-ch:
		return result.data, result.err
	case <-time.After(6 * time.Second):
		return nil, errors.New("DNS via exit-node timeout")
	}
}

func (d *DNSMuxTransport) handleReceive(data []byte) {
	if len(data) < dnsMuxHeader || !bytes.Equal(data[:len(dnsMuxMagic)], dnsMuxMagic) {
		d.receiverMu.RLock()
		callback := d.receiver
		d.receiverMu.RUnlock()

		if callback != nil {
			callback(data)
		}
		return
	}

	messageType := data[6]
	id := binary.BigEndian.Uint32(data[7:11])
	payload := append([]byte(nil), data[11:]...)

	switch messageType {
	case dnsMuxRequest:
		if d.exitNode {
			go d.handleDNSRequest(id, payload)
		}

	case dnsMuxResponse:
		if !d.exitNode {
			d.deliverResult(id, dnsMuxResult{data: payload})
		}

	case dnsMuxError:
		if !d.exitNode {
			d.deliverResult(id, dnsMuxResult{err: errors.New(string(payload))})
		}
	}
}

func (d *DNSMuxTransport) deliverResult(id uint32, result dnsMuxResult) {
	d.pendingMu.Lock()
	ch := d.pending[id]
	d.pendingMu.Unlock()

	if ch != nil {
		select {
		case ch <- result:
		default:
		}
	}
}

func (d *DNSMuxTransport) handleDNSRequest(id uint32, payload []byte) {
	if len(payload) < 2 {
		_ = d.sendControl(dnsMuxError, id, []byte("malformed DNS request"))
		return
	}

	serverLength := int(payload[0])
	if serverLength == 0 || len(payload) < 1+serverLength+12 {
		_ = d.sendControl(dnsMuxError, id, []byte("malformed DNS request"))
		return
	}

	server := string(payload[1 : 1+serverLength])
	query := payload[1+serverLength:]

	ip := net.ParseIP(server)
	if ip == nil {
		_ = d.sendControl(dnsMuxError, id, []byte("invalid DNS server"))
		return
	}

	answer, err := queryDNSFromExit(ip.String(), query)
	if err != nil {
		_ = d.sendControl(dnsMuxError, id, []byte(err.Error()))
		return
	}

	_ = d.sendControl(dnsMuxResponse, id, answer)
}

func (d *DNSMuxTransport) sendControl(messageType byte, id uint32, payload []byte) error {
	frame := make([]byte, dnsMuxHeader+len(payload))
	copy(frame, dnsMuxMagic)
	frame[6] = messageType
	binary.BigEndian.PutUint32(frame[7:11], id)
	copy(frame[11:], payload)
	return d.Transport.Send(frame)
}

func queryDNSFromExit(server string, query []byte) ([]byte, error) {
	address := net.JoinHostPort(server, "53")

	conn, err := net.DialTimeout("udp", address, 4*time.Second)
	if err != nil {
		return nil, fmt.Errorf("DNS UDP connect: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	if _, err := conn.Write(query); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("DNS UDP write: %w", err)
	}

	buffer := make([]byte, 65535)
	n, err := conn.Read(buffer)
	_ = conn.Close()
	if err != nil {
		return nil, fmt.Errorf("DNS UDP read: %w", err)
	}
	if n < 12 {
		return nil, errors.New("short DNS response")
	}

	answer := append([]byte(nil), buffer[:n]...)

	// TC bit: retry over TCP if the UDP response was truncated.
	if answer[2]&0x02 != 0 {
		return queryDNSTCP(address, query)
	}

	return answer, nil
}

func queryDNSTCP(address string, query []byte) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", address, 4*time.Second)
	if err != nil {
		return nil, fmt.Errorf("DNS TCP connect: %w", err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	request := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(request[:2], uint16(len(query)))
	copy(request[2:], query)

	if _, err := conn.Write(request); err != nil {
		return nil, fmt.Errorf("DNS TCP write: %w", err)
	}

	var sizeBytes [2]byte
	if _, err := io.ReadFull(conn, sizeBytes[:]); err != nil {
		return nil, fmt.Errorf("DNS TCP size: %w", err)
	}

	size := int(binary.BigEndian.Uint16(sizeBytes[:]))
	if size < 12 {
		return nil, errors.New("short DNS TCP response")
	}

	answer := make([]byte, size)
	if _, err := io.ReadFull(conn, answer); err != nil {
		return nil, fmt.Errorf("DNS TCP read: %w", err)
	}

	return answer, nil
}
