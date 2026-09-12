package yandex

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"universal-bypass-tool/transport"
	"universal-bypass-tool/utils"
)

// resilientVolgaSession is deliberately small so the supervisor can be tested
// without talking to Yandex. YandexVolgaTransport satisfies it directly.
type resilientVolgaSession interface {
	transport.Transport
}

type resilientVolgaFactory func(string, transport.TransportConfig) resilientVolgaSession

// ResilientYandexVolgaTransport supervises short-lived Volga/Xiva sessions.
//
// The underlying YandexVolgaTransport already retries a dropped websocket with
// the same Xiva credentials. That is enough for a short network interruption,
// but not for an expired/invalid Volga session: the process stays alive while
// IsConnected() remains false forever. This wrapper gives the existing session
// a short grace period to recover, then tears it down, repeats the full public
// document bootstrap/auth flow, and installs a fresh transport instance.
//
// It intentionally sits below encryption/compression/multiplexing. Replacing a
// carrier session therefore does not rotate OpenFlux encryption keys or client
// IDs and does not require rebuilding the gVisor/TUN stack.
type ResilientYandexVolgaTransport struct {
	*transport.BaseTransport

	docURL string
	cfg    transport.TransportConfig
	make   resilientVolgaFactory

	mu       sync.RWMutex
	inner    resilientVolgaSession
	receiver func([]byte)

	lifecycleMu sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	healthInterval    time.Duration
	disconnectedGrace time.Duration
	reconnectMinDelay time.Duration
	reconnectMaxDelay time.Duration
	reconnectFactor   float64

	reconnects atomic.Uint64
}

func NewResilientYandexVolgaTransport(docURL string, cfg transport.TransportConfig) *ResilientYandexVolgaTransport {
	return newResilientYandexVolgaTransport(docURL, cfg, func(url string, tc transport.TransportConfig) resilientVolgaSession {
		return NewYandexVolgaTransport(url, tc)
	})
}

func newResilientYandexVolgaTransport(docURL string, cfg transport.TransportConfig, factory resilientVolgaFactory) *ResilientYandexVolgaTransport {
	return &ResilientYandexVolgaTransport{
		BaseTransport:     transport.NewBaseTransport(cfg),
		docURL:            strings.TrimSpace(docURL),
		cfg:               cfg,
		make:              factory,
		healthInterval:    500 * time.Millisecond,
		disconnectedGrace: 5 * time.Second,
		reconnectMinDelay: 1 * time.Second,
		reconnectMaxDelay: 30 * time.Second,
		reconnectFactor:   2,
	}
}

func (t *ResilientYandexVolgaTransport) Start() error {
	if t.docURL == "" {
		return errors.New("Volga document URL is empty")
	}
	if t.IsRunning() {
		return nil
	}
	if err := t.BaseTransport.Start(); err != nil {
		return err
	}

	t.ctx, t.cancel = context.WithCancel(context.Background())

	session, err := t.startSession(t.ctx)
	if err != nil {
		t.cancel()
		_ = t.BaseTransport.Stop()
		return err
	}
	t.installSession(session)

	t.wg.Add(1)
	go t.supervise(t.ctx)
	return nil
}

func (t *ResilientYandexVolgaTransport) Stop() error {
	if !t.IsRunning() {
		return nil
	}

	if t.cancel != nil {
		t.cancel()
	}

	// Serialize against a full re-auth cycle. startSession observes ctx.Done(),
	// so this does not wait for the inner HTTP timeouts during shutdown.
	t.lifecycleMu.Lock()
	old := t.detachSession()
	if old != nil {
		_ = old.Stop()
	}
	t.lifecycleMu.Unlock()

	t.wg.Wait()
	return t.BaseTransport.Stop()
}

func (t *ResilientYandexVolgaTransport) Send(data []byte) error {
	t.mu.RLock()
	inner := t.inner
	t.mu.RUnlock()
	if inner == nil || !inner.IsConnected() || !t.IsConnected() {
		return errors.New("Volga transport not connected")
	}
	return inner.Send(data)
}

func (t *ResilientYandexVolgaTransport) Receive(callback func([]byte)) {
	t.mu.Lock()
	t.receiver = callback
	t.mu.Unlock()
}

func (t *ResilientYandexVolgaTransport) Stats() transport.TransportStats {
	t.mu.RLock()
	inner := t.inner
	t.mu.RUnlock()

	if inner == nil {
		base := t.BaseTransport.Stats()
		base.Reconnects = t.reconnects.Load()
		base.Connected = t.IsConnected()
		return base
	}

	stats := inner.Stats()
	stats.Reconnects += t.reconnects.Load()
	stats.Connected = t.IsConnected()
	stats.Uptime = t.BaseTransport.Stats().Uptime
	return stats
}

func (t *ResilientYandexVolgaTransport) supervise(ctx context.Context) {
	defer t.wg.Done()
	ticker := time.NewTicker(t.healthInterval)
	defer ticker.Stop()

	var disconnectedSince time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			t.mu.RLock()
			inner := t.inner
			t.mu.RUnlock()

			if inner != nil && inner.IsConnected() {
				t.SetConnected(true)
				disconnectedSince = time.Time{}
				continue
			}

			t.SetConnected(false)
			if disconnectedSince.IsZero() {
				disconnectedSince = now
				continue
			}
			if now.Sub(disconnectedSince) < t.disconnectedGrace {
				continue
			}

			t.recoverSession(ctx, fmt.Sprintf("carrier disconnected for %s", now.Sub(disconnectedSince).Round(time.Second)))
			disconnectedSince = time.Time{}
		}
	}
}

func (t *ResilientYandexVolgaTransport) recoverSession(ctx context.Context, reason string) {
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()

	if ctx.Err() != nil || !t.IsRunning() {
		return
	}

	t.eventf("[VOLGA] session unhealthy: %s; full re-auth required", reason)

	old := t.detachSession()
	if old != nil {
		_ = old.Stop()
	}

	delay := t.reconnectMinDelay
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil || !t.IsRunning() {
			return
		}

		t.eventf("[VOLGA] re-auth attempt %d", attempt)
		session, err := t.startSession(ctx)
		if err == nil {
			t.installSession(session)
			t.reconnects.Add(1)
			t.eventf("[VOLGA] transport restored after full re-auth")
			return
		}

		t.eventf("[VOLGA] re-auth attempt %d failed: %v", attempt, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		delay = time.Duration(float64(delay) * t.reconnectFactor)
		if delay > t.reconnectMaxDelay {
			delay = t.reconnectMaxDelay
		}
	}
}

func (t *ResilientYandexVolgaTransport) startSession(ctx context.Context) (resilientVolgaSession, error) {
	session := t.make(t.docURL, t.cfg)
	if session == nil {
		return nil, errors.New("Volga session factory returned nil")
	}

	// The callback is stable across carrier replacements. Guard it with an
	// identity check so a late packet from a session being torn down cannot be
	// injected into the new tunnel.
	session.Receive(func(data []byte) {
		t.mu.RLock()
		active := t.inner == session
		callback := t.receiver
		t.mu.RUnlock()
		if active && callback != nil {
			callback(data)
		}
	})

	result := make(chan error, 1)
	go func() {
		result <- session.Start()
	}()

	select {
	case err := <-result:
		if err != nil {
			_ = session.Stop()
			return nil, err
		}
		if !session.IsConnected() {
			_ = session.Stop()
			return nil, errors.New("Volga session started but is not connected")
		}
		return session, nil
	case <-ctx.Done():
		// Start() uses bounded HTTP/WS timeouts but does not accept our wrapper
		// context. Do not make Stop() wait for those timeouts; clean it up once
		// the in-flight Start() returns.
		go func() {
			<-result
			_ = session.Stop()
		}()
		return nil, ctx.Err()
	}
}

func (t *ResilientYandexVolgaTransport) installSession(session resilientVolgaSession) {
	t.mu.Lock()
	t.inner = session
	t.mu.Unlock()
	t.SetConnected(session != nil && session.IsConnected())
}

func (t *ResilientYandexVolgaTransport) detachSession() resilientVolgaSession {
	t.SetConnected(false)
	t.mu.Lock()
	old := t.inner
	t.inner = nil
	t.mu.Unlock()
	return old
}

func (t *ResilientYandexVolgaTransport) eventf(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	// log.Printf is visible in journald even when the CLI was not started with
	// --debug; Debugf additionally feeds the Android in-app log sink.
	log.Print(message)
	utils.Debugf("%s", message)
}
