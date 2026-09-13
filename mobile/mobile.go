// Package mobile exposes the OpenFlux packet transport to Android through
// gomobile. Android owns the TUN file descriptor; this package only transports
// complete IPv4 packets through the encrypted Yandex Volga carrier.
package mobile

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"universal-bypass-tool/transport"
	"universal-bypass-tool/transport/yandex"
	"universal-bypass-tool/utils"
)

var client = packetClient{}

type packetClient struct {
	mu             sync.Mutex
	running        bool
	transport      transport.Transport
	encrypted      *transport.EncryptedTransport
	carrier        *yandex.ResilientYandexVolgaTransport
	watchdogCancel context.CancelFunc
	packetQueue    chan []byte
	readStop       chan struct{}
	logs           []string
}

func appendLog(message string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.logs = append(client.logs, message)
	if len(client.logs) > 500 {
		client.logs = append([]string(nil), client.logs[len(client.logs)-500:]...)
	}
}

// Start connects the packet transport. It returns an empty string on success
// and a user-readable error on failure.
func Start(documentURL, encryptionSecret string) string {
	if documentURL == "" {
		return "Ссылка на документ не указана"
	}
	if len(encryptionSecret) < 16 {
		return "Ключ шифрования должен содержать не менее 16 символов"
	}

	queueSize := transport.DefaultConfig().MaxQueueSize
	if queueSize < 1 {
		queueSize = 1
	}
	packetQueue := make(chan []byte, queueSize)
	readStop := make(chan struct{})

	client.mu.Lock()
	if client.running {
		client.mu.Unlock()
		return ""
	}
	client.running = true
	client.packetQueue = packetQueue
	client.readStop = readStop
	client.logs = nil
	client.encrypted = nil
	client.mu.Unlock()

	utils.EnableDebug()
	utils.SetLogSink(appendLog)
	appendLog("[ANDROID] Запуск транспорта Yandex Volga")

	config := transport.DefaultConfig()
	carrier := yandex.NewResilientYandexVolgaTransport(documentURL, config)
	encrypted, err := transport.NewEncryptedTransport(
		carrier, encryptionSecret, documentURL, false,
	)
	if err != nil {
		client.mu.Lock()
		client.running = false
		client.mu.Unlock()
		return err.Error()
	}
	compressed := transport.NewCompressedTransport(encrypted)
	multiplexed, err := transport.NewMultiplexTransport(compressed, false)
	if err != nil {
		client.mu.Lock()
		client.running = false
		client.mu.Unlock()
		return err.Error()
	}
	appendLog(fmt.Sprintf("[ANDROID] Multi-client ID: %s", multiplexed.ClientID()))
	var trans transport.Transport = multiplexed
	trans = transport.NewDNSMuxTransport(trans, false)
	trans.Receive(func(data []byte) {
		packet := append([]byte(nil), data...)
		enqueuePacket(packetQueue, readStop, packet)
	})

	if err := trans.Start(); err != nil {
		appendLog(fmt.Sprintf("[ANDROID] Ошибка запуска: %v", err))
		client.mu.Lock()
		client.running = false
		client.mu.Unlock()
		return err.Error()
	}

	watchdogCtx, watchdogCancel := context.WithCancel(context.Background())

	client.mu.Lock()
	client.transport = trans
	client.encrypted = encrypted
	client.carrier = carrier
	client.watchdogCancel = watchdogCancel
	client.mu.Unlock()

	go superviseLiveness(watchdogCtx, encrypted, carrier)
	return ""
}

func Stop() {
	client.mu.Lock()
	trans := client.transport
	cancel := client.watchdogCancel
	readStop := client.readStop
	client.running = false
	client.transport = nil
	client.encrypted = nil
	client.carrier = nil
	client.watchdogCancel = nil
	client.packetQueue = nil
	client.readStop = nil
	client.mu.Unlock()

	if readStop != nil {
		close(readStop)
	}
	if cancel != nil {
		cancel()
	}

	appendLog("[ANDROID] Остановка транспорта")
	if trans != nil {
		_ = trans.Stop()
	}
}

const (
	livenessProbeInterval = 30 * time.Second
	livenessFailureWindow = 90 * time.Second
)

func superviseLiveness(
	ctx context.Context,
	encrypted *transport.EncryptedTransport,
	carrier *yandex.ResilientYandexVolgaTransport,
) {
	ticker := time.NewTicker(livenessProbeInterval)
	defer ticker.Stop()

	lastSequence := encrypted.PingSequence()
	lastPong := time.Now()

	probe := func(now time.Time) {
		sequence := encrypted.PingSequence()
		if sequence != lastSequence {
			lastSequence = sequence
			lastPong = now
		}

		if err := encrypted.Ping(); err != nil {
			appendLog(fmt.Sprintf("[ANDROID] Liveness ping send failed: %v", err))
		}

		if now.Sub(lastPong) >= livenessFailureWindow {
			appendLog(fmt.Sprintf(
				"[ANDROID] Liveness timeout: no encrypted pong for %s; rebuilding Volga session",
				now.Sub(lastPong).Round(time.Second),
			))
			carrier.ForceReconnect("Android end-to-end encrypted ping timeout")
			// Give the newly requested bootstrap cycle a full health window.
			lastPong = now
		}
	}

	// Do not wait one full interval before establishing the first baseline.
	probe(time.Now())

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			probe(now)
		}
	}
}

func Ping() string {
	client.mu.Lock()
	encrypted := client.encrypted
	running := client.running
	client.mu.Unlock()
	if !running || encrypted == nil {
		return "Транспорт не запущен"
	}
	if err := encrypted.Ping(); err != nil {
		return err.Error()
	}
	return ""
}

func PingMillis() int64 {
	client.mu.Lock()
	encrypted := client.encrypted
	client.mu.Unlock()
	if encrypted == nil {
		return -1
	}
	return encrypted.LastPingMillis()
}

func PingSequence() int64 {
	client.mu.Lock()
	encrypted := client.encrypted
	client.mu.Unlock()
	if encrypted == nil {
		return 0
	}
	return encrypted.PingSequence()
}

func IsConnected() bool {
	client.mu.Lock()
	trans := client.transport
	client.mu.Unlock()
	return trans != nil && trans.IsConnected()
}

func Send(packet []byte) string {
	client.mu.Lock()
	trans := client.transport
	running := client.running
	client.mu.Unlock()
	if !running || trans == nil {
		return "Транспорт не запущен"
	}
	if err := trans.Send(packet); err != nil {
		return err.Error()
	}
	return ""
}

// ResolveDNS sends a raw DNS wire-format query through the active
// OpenFlux transport and resolves it on the exit node.
func ResolveDNS(query []byte, dnsServer string) []byte {
	client.mu.Lock()
	trans := client.transport
	running := client.running
	client.mu.Unlock()

	if !running || trans == nil {
		appendLog("[ANDROID] DNS: транспорт не запущен")
		return nil
	}

	mux, ok := trans.(*transport.DNSMuxTransport)
	if !ok {
		appendLog("[ANDROID] DNS: DNS mux не активен")
		return nil
	}

	answer, err := mux.QueryDNS(query, strings.TrimSpace(dnsServer))
	if err != nil {
		appendLog(fmt.Sprintf("[ANDROID] DNS через exit-node: %v", err))
		return nil
	}
	return answer
}

func enqueuePacket(packetQueue chan []byte, readStop <-chan struct{}, packet []byte) {
	select {
	case <-readStop:
		return
	default:
	}

	select {
	case packetQueue <- packet:
		return
	default:
	}

	// Queue full: discard the oldest packet and keep the newest traffic.
	select {
	case <-packetQueue:
	default:
	}

	select {
	case packetQueue <- packet:
	case <-readStop:
	default:
	}
}

func readPacket(packetQueue <-chan []byte, readStop <-chan struct{}) []byte {
	select {
	case packet := <-packetQueue:
		return packet
	case <-readStop:
		return nil
	}
}

// Read blocks until a received packet is available or the Android VPN session
// stops. This avoids waking Java/Go hundreds of times per second while idle.
func Read() []byte {
	client.mu.Lock()
	packetQueue := client.packetQueue
	readStop := client.readStop
	running := client.running
	client.mu.Unlock()

	if !running || packetQueue == nil || readStop == nil {
		return nil
	}

	return readPacket(packetQueue, readStop)
}

// ReadLogs returns and clears the pending log lines.
func ReadLogs() string {
	client.mu.Lock()
	defer client.mu.Unlock()
	logs := strings.Join(client.logs, "\n")
	client.logs = nil
	return logs
}
