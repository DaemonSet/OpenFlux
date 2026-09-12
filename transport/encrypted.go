package transport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/scrypt"
)

const (
	encryptedVersion  = byte(1)
	encryptedHeader   = 5
	maxSeenNonces     = 4096
	frameData         = byte(0)
	framePingRequest  = byte(1)
	framePingResponse = byte(2)
)

var encryptedMagic = [3]byte{'O', 'F', 'X'}

// EncryptedTransport provides end-to-end authenticated encryption over an
// untrusted transport. The document URL is used only as a public, per-document
// KDF salt; secrecy comes exclusively from the shared secret.
type EncryptedTransport struct {
	Transport
	sendAEAD         cipher.AEAD
	receiveAEAD      cipher.AEAD
	sendDirection    byte
	recvDirection    byte
	seenMu           sync.Mutex
	seen             map[string]struct{}
	seenOrder        []string
	pingMu           sync.Mutex
	pendingPings     map[uint64]time.Time
	lastPingMs       atomic.Int64
	pingSequence     atomic.Int64
	peerPingSequence atomic.Int64
}

// NewEncryptedTransport creates a directional AES-256-GCM transport. Both
// peers must use the same secret and context, and exactly one peer must set
// exitNode=true.
func NewEncryptedTransport(inner Transport, secret, context string, exitNode bool) (*EncryptedTransport, error) {
	if inner == nil {
		return nil, errors.New("inner transport is nil")
	}
	if len(secret) < 16 {
		return nil, errors.New("encryption secret must contain at least 16 characters")
	}

	salt := sha256.Sum256([]byte("OpenFlux encrypted transport v1\x00" + context))
	master, err := scrypt.Key([]byte(secret), salt[:], 32768, 8, 1, 32)
	if err != nil {
		return nil, fmt.Errorf("derive encryption key: %w", err)
	}
	clientToExit := deriveDirectionalKey(master, "client-to-exit")
	exitToClient := deriveDirectionalKey(master, "exit-to-client")

	sendKey, receiveKey := clientToExit, exitToClient
	sendDirection, receiveDirection := byte(0), byte(1)
	if exitNode {
		sendKey, receiveKey = exitToClient, clientToExit
		sendDirection, receiveDirection = 1, 0
	}
	sendAEAD, err := newGCM(sendKey)
	if err != nil {
		return nil, err
	}
	receiveAEAD, err := newGCM(receiveKey)
	if err != nil {
		return nil, err
	}
	return &EncryptedTransport{
		Transport:     inner,
		sendAEAD:      sendAEAD,
		receiveAEAD:   receiveAEAD,
		sendDirection: sendDirection,
		recvDirection: receiveDirection,
		seen:          make(map[string]struct{}),
		pendingPings:  make(map[uint64]time.Time),
	}, nil
}

func deriveDirectionalKey(master []byte, label string) []byte {
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte("OpenFlux direction v1\x00" + label))
	return mac.Sum(nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	return aead, nil
}

func (e *EncryptedTransport) Send(data []byte) error {
	frame := make([]byte, 1, len(data)+1)
	frame[0] = frameData
	frame = append(frame, data...)
	return e.sendFrame(frame)
}

func (e *EncryptedTransport) sendFrame(data []byte) error {
	header := []byte{encryptedMagic[0], encryptedMagic[1], encryptedMagic[2], encryptedVersion, e.sendDirection}
	nonce := make([]byte, e.sendAEAD.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("create packet nonce: %w", err)
	}
	packet := make([]byte, 0, len(header)+len(nonce)+len(data)+e.sendAEAD.Overhead())
	packet = append(packet, header...)
	packet = append(packet, nonce...)
	packet = e.sendAEAD.Seal(packet, nonce, data, header)
	return e.Transport.Send(packet)
}

func (e *EncryptedTransport) Receive(callback func([]byte)) {
	e.Transport.Receive(func(packet []byte) {
		if len(packet) < encryptedHeader+e.receiveAEAD.NonceSize()+e.receiveAEAD.Overhead() {
			return
		}
		header := packet[:encryptedHeader]
		if header[0] != encryptedMagic[0] || header[1] != encryptedMagic[1] ||
			header[2] != encryptedMagic[2] || header[3] != encryptedVersion ||
			header[4] != e.recvDirection {
			return
		}
		nonceEnd := encryptedHeader + e.receiveAEAD.NonceSize()
		nonce := packet[encryptedHeader:nonceEnd]
		plaintext, err := e.receiveAEAD.Open(nil, nonce, packet[nonceEnd:], header)
		if err != nil || !e.rememberNonce(nonce) {
			return
		}
		if len(plaintext) == 0 {
			return
		}
		switch plaintext[0] {
		case frameData:
			callback(plaintext[1:])
		case framePingRequest:
			if len(plaintext) == 9 {
				e.peerPingSequence.Add(1)
				response := append([]byte{framePingResponse}, plaintext[1:]...)
				_ = e.sendFrame(response)
			}
		case framePingResponse:
			if len(plaintext) == 9 {
				e.completePing(binary.BigEndian.Uint64(plaintext[1:]))
			}
		}
	})
}

// Ping measures a round trip through the encrypted document transport and the
// remote OpenFlux peer. It does not use ICMP or bypass the VPN protocol.
func (e *EncryptedTransport) Ping() error {
	var tokenBytes [8]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return fmt.Errorf("create ping token: %w", err)
	}
	token := binary.BigEndian.Uint64(tokenBytes[:])
	now := time.Now()
	e.pingMu.Lock()
	for pending, started := range e.pendingPings {
		if now.Sub(started) > 30*time.Second {
			delete(e.pendingPings, pending)
		}
	}
	e.pendingPings[token] = now
	e.pingMu.Unlock()
	frame := append([]byte{framePingRequest}, tokenBytes[:]...)
	if err := e.sendFrame(frame); err != nil {
		e.pingMu.Lock()
		delete(e.pendingPings, token)
		e.pingMu.Unlock()
		return err
	}
	return nil
}

func (e *EncryptedTransport) completePing(token uint64) {
	e.pingMu.Lock()
	started, ok := e.pendingPings[token]
	delete(e.pendingPings, token)
	e.pingMu.Unlock()
	if !ok {
		return
	}
	milliseconds := time.Since(started).Milliseconds()
	if milliseconds < 1 {
		milliseconds = 1
	}
	e.lastPingMs.Store(milliseconds)
	e.pingSequence.Add(1)
}

func (e *EncryptedTransport) LastPingMillis() int64 {
	if e.pingSequence.Load() == 0 {
		return -1
	}
	return e.lastPingMs.Load()
}

func (e *EncryptedTransport) PingSequence() int64 {
	return e.pingSequence.Load()
}

func (e *EncryptedTransport) PeerPingSequence() int64 {
	return e.peerPingSequence.Load()
}

func (e *EncryptedTransport) rememberNonce(nonce []byte) bool {
	key := string(nonce)
	e.seenMu.Lock()
	defer e.seenMu.Unlock()
	if _, exists := e.seen[key]; exists {
		return false
	}
	e.seen[key] = struct{}{}
	e.seenOrder = append(e.seenOrder, key)
	if len(e.seenOrder) > maxSeenNonces {
		oldest := e.seenOrder[0]
		e.seenOrder = e.seenOrder[1:]
		delete(e.seen, oldest)
	}
	return true
}
