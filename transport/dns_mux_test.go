package transport

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

type dnsMuxTestTransport struct {
	mu     sync.Mutex
	recv   func([]byte)
	onSend func([]byte)
	sent   [][]byte
}

func (d *dnsMuxTestTransport) Start() error { return nil }

func (d *dnsMuxTestTransport) Stop() error { return nil }

func (d *dnsMuxTestTransport) Send(data []byte) error {
	packet := append([]byte(nil), data...)

	d.mu.Lock()
	d.sent = append(d.sent, packet)
	onSend := d.onSend
	d.mu.Unlock()

	if onSend != nil {
		onSend(packet)
	}

	return nil
}

func (d *dnsMuxTestTransport) Receive(callback func([]byte)) {
	d.mu.Lock()
	d.recv = callback
	d.mu.Unlock()
}

func (d *dnsMuxTestTransport) IsConnected() bool { return true }

func (d *dnsMuxTestTransport) Stats() TransportStats {
	return TransportStats{Connected: true}
}

func (d *dnsMuxTestTransport) setOnSend(callback func([]byte)) {
	d.mu.Lock()
	d.onSend = callback
	d.mu.Unlock()
}

func (d *dnsMuxTestTransport) inject(data []byte) {
	packet := append([]byte(nil), data...)

	d.mu.Lock()
	callback := d.recv
	d.mu.Unlock()

	if callback != nil {
		callback(packet)
	}
}

func TestDNSMuxPassesOrdinaryFramesThrough(t *testing.T) {
	inner := &dnsMuxTestTransport{}
	mux := NewDNSMuxTransport(inner, false)

	delivered := make(chan []byte, 1)
	mux.Receive(func(data []byte) {
		delivered <- append([]byte(nil), data...)
	})

	packet := []byte{
		0x45, 0x00, 0x00, 0x14,
		0x00, 0x00, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00,
		10, 10, 10, 2,
		1, 1, 1, 1,
	}

	inner.inject(packet)

	select {
	case got := <-delivered:
		if !bytes.Equal(got, packet) {
			t.Fatalf("ordinary frame changed: got=%x want=%x", got, packet)
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary frame was not delivered")
	}
}

func TestDNSMuxQueryRoundTrip(t *testing.T) {
	inner := &dnsMuxTestTransport{}
	mux := NewDNSMuxTransport(inner, false)

	query := []byte{
		0x12, 0x34,
		0x01, 0x00,
		0x00, 0x01,
		0x00, 0x00,
		0x00, 0x00,
		0x00, 0x00,
	}

	answer := []byte{
		0x12, 0x34,
		0x81, 0x80,
		0x00, 0x01,
		0x00, 0x00,
		0x00, 0x00,
		0x00, 0x00,
	}

	const server = "94.140.14.14"

	inner.setOnSend(func(frame []byte) {
		if len(frame) < dnsMuxHeader+2 {
			t.Fatalf("short DNSMux request: %d", len(frame))
		}

		if !bytes.Equal(frame[:len(dnsMuxMagic)], dnsMuxMagic) {
			t.Fatalf("bad DNSMux magic: %x", frame[:len(dnsMuxMagic)])
		}

		if frame[6] != dnsMuxRequest {
			t.Fatalf("message type=%d want request", frame[6])
		}

		id := binary.BigEndian.Uint32(frame[7:11])
		if id == 0 {
			t.Fatal("DNSMux request ID must not be zero")
		}

		payload := frame[dnsMuxHeader:]
		serverLength := int(payload[0])

		if len(payload) < 1+serverLength {
			t.Fatalf("short DNSMux payload: %d", len(payload))
		}

		gotServer := string(payload[1 : 1+serverLength])
		if gotServer != server {
			t.Fatalf("server=%q want=%q", gotServer, server)
		}

		gotQuery := payload[1+serverLength:]
		if !bytes.Equal(gotQuery, query) {
			t.Fatalf("query changed: got=%x want=%x", gotQuery, query)
		}

		response := make([]byte, dnsMuxHeader+len(answer))
		copy(response, dnsMuxMagic)
		response[6] = dnsMuxResponse
		binary.BigEndian.PutUint32(response[7:11], id)
		copy(response[dnsMuxHeader:], answer)

		inner.inject(response)
	})

	got, err := mux.QueryDNS(query, server)
	if err != nil {
		t.Fatalf("QueryDNS: %v", err)
	}

	if !bytes.Equal(got, answer) {
		t.Fatalf("answer changed: got=%x want=%x", got, answer)
	}
}

func TestDNSMuxRejectsInvalidQueryAndServer(t *testing.T) {
	inner := &dnsMuxTestTransport{}
	mux := NewDNSMuxTransport(inner, false)

	if _, err := mux.QueryDNS(make([]byte, 11), "1.1.1.1"); err == nil {
		t.Fatal("short DNS query was accepted")
	}

	if _, err := mux.QueryDNS(make([]byte, 12), "not-an-ip"); err == nil {
		t.Fatal("invalid DNS server was accepted")
	}
}

func TestDNSMuxStopUnblocksPendingQuery(t *testing.T) {
	inner := &dnsMuxTestTransport{}
	mux := NewDNSMuxTransport(inner, false)

	requestSent := make(chan struct{}, 1)

	inner.setOnSend(func([]byte) {
		select {
		case requestSent <- struct{}{}:
		default:
		}
	})

	query := make([]byte, 12)
	result := make(chan error, 1)

	go func() {
		_, err := mux.QueryDNS(query, "1.1.1.1")
		result <- err
	}()

	select {
	case <-requestSent:
	case <-time.After(time.Second):
		t.Fatal("DNS request was not sent")
	}

	if err := mux.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("pending query returned success after Stop")
		}
		if err.Error() != "transport stopped" {
			t.Fatalf("pending query error=%q want=%q", err, "transport stopped")
		}
	case <-time.After(time.Second):
		t.Fatal("pending DNS query was not released by Stop")
	}
}
