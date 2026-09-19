package yandex

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"universal-bypass-tool/transport"
)

type fakeResilientVolgaSession struct {
	connected atomic.Bool
	stopped   atomic.Bool
	startErr  error

	mu       sync.Mutex
	receiver func([]byte)
}

func (f *fakeResilientVolgaSession) Start() error {
	if f.startErr != nil {
		return f.startErr
	}
	f.connected.Store(true)
	return nil
}
func (f *fakeResilientVolgaSession) Stop() error {
	f.stopped.Store(true)
	f.connected.Store(false)
	return nil
}
func (f *fakeResilientVolgaSession) Send([]byte) error {
	if !f.connected.Load() {
		return errors.New("disconnected")
	}
	return nil
}
func (f *fakeResilientVolgaSession) Receive(cb func([]byte)) {
	f.mu.Lock()
	f.receiver = cb
	f.mu.Unlock()
}
func (f *fakeResilientVolgaSession) IsConnected() bool { return f.connected.Load() }
func (f *fakeResilientVolgaSession) Stats() transport.TransportStats {
	return transport.TransportStats{Connected: f.connected.Load()}
}

func TestResilientVolgaFullReauthAfterPersistentDisconnect(t *testing.T) {
	var mu sync.Mutex
	var made []*fakeResilientVolgaSession
	factory := func(string, transport.TransportConfig) resilientVolgaSession {
		s := &fakeResilientVolgaSession{}
		mu.Lock()
		made = append(made, s)
		mu.Unlock()
		return s
	}

	r := newResilientYandexVolgaTransport("https://example.invalid/doc", transport.DefaultConfig(), factory)
	r.healthInterval = 5 * time.Millisecond
	r.disconnectedGrace = 20 * time.Millisecond
	r.reconnectMinDelay = 1 * time.Millisecond
	r.reconnectMaxDelay = 5 * time.Millisecond

	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	mu.Lock()
	first := made[0]
	mu.Unlock()
	first.connected.Store(false)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(made)
		var secondConnected bool
		if count >= 2 {
			secondConnected = made[1].connected.Load()
		}
		mu.Unlock()
		if count >= 2 && secondConnected && r.IsConnected() {
			if r.reconnects.Load() != 1 {
				t.Fatalf("reconnects=%d want 1", r.reconnects.Load())
			}
			if !first.stopped.Load() {
				t.Fatal("old Volga session was not stopped")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("resilient transport did not rebuild the Volga session")
}

func TestResilientVolgaLetsShortDisconnectRecoverInPlace(t *testing.T) {
	var mu sync.Mutex
	var made []*fakeResilientVolgaSession
	factory := func(string, transport.TransportConfig) resilientVolgaSession {
		s := &fakeResilientVolgaSession{}
		mu.Lock()
		made = append(made, s)
		mu.Unlock()
		return s
	}

	r := newResilientYandexVolgaTransport("https://example.invalid/doc", transport.DefaultConfig(), factory)
	r.healthInterval = 5 * time.Millisecond
	r.disconnectedGrace = 80 * time.Millisecond

	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	mu.Lock()
	first := made[0]
	mu.Unlock()
	first.connected.Store(false)
	time.Sleep(20 * time.Millisecond)
	first.connected.Store(true)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(made)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("created %d sessions for a transient disconnect; want 1", count)
	}
	if r.reconnects.Load() != 0 {
		t.Fatalf("full reauth count=%d want 0", r.reconnects.Load())
	}
	if !r.IsConnected() {
		t.Fatal("transport did not return to connected state")
	}
}

func TestResilientVolgaExitBoundsXivaConnectionAge(t *testing.T) {
	exit := NewResilientYandexVolgaExitTransport("https://example.invalid/doc", transport.DefaultConfig())
	exitSession, ok := exit.make(exit.docURL, exit.cfg).(*YandexVolgaTransport)
	if !ok {
		t.Fatalf("exit session type=%T want *YandexVolgaTransport", exit.make(exit.docURL, exit.cfg))
	}
	if exitSession.cfg.WSMaxConnectionAge != exitVolgaWSMaxConnectionAge {
		t.Fatalf("exit websocket max age=%s want=%s", exitSession.cfg.WSMaxConnectionAge, exitVolgaWSMaxConnectionAge)
	}

	client := NewResilientYandexVolgaTransport("https://example.invalid/doc", transport.DefaultConfig())
	clientSession, ok := client.make(client.docURL, client.cfg).(*YandexVolgaTransport)
	if !ok {
		t.Fatalf("client session type=%T want *YandexVolgaTransport", client.make(client.docURL, client.cfg))
	}
	if clientSession.cfg.WSMaxConnectionAge != 0 {
		t.Fatalf("client websocket max age=%s want disabled", clientSession.cfg.WSMaxConnectionAge)
	}
}

func TestResilientVolgaForcedRecoveryReplacesConnectedSession(t *testing.T) {
	var mu sync.Mutex
	var made []*fakeResilientVolgaSession
	factory := func(string, transport.TransportConfig) resilientVolgaSession {
		session := &fakeResilientVolgaSession{}
		mu.Lock()
		made = append(made, session)
		mu.Unlock()
		return session
	}

	r := newResilientYandexVolgaTransport(
		"https://example.invalid/doc",
		transport.DefaultConfig(),
		factory,
	)
	r.reconnectMinDelay = time.Millisecond
	r.reconnectMaxDelay = 5 * time.Millisecond

	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	mu.Lock()
	first := made[0]
	mu.Unlock()

	if !r.ForceRecover("unit test") {
		t.Fatal("forced recovery request was rejected")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(made)
		var secondConnected bool
		if count >= 2 {
			secondConnected = made[1].connected.Load()
		}
		mu.Unlock()

		if count >= 2 && secondConnected && r.IsConnected() {
			if !first.stopped.Load() {
				t.Fatal("old Volga session was not stopped")
			}
			if got := r.reconnects.Load(); got != 1 {
				t.Fatalf("reconnects=%d want 1", got)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("forced recovery did not replace the connected Volga session")
}
