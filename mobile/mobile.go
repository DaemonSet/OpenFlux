// Package mobile exposes the OpenFlux packet transport to Android through
// gomobile. Android owns the TUN file descriptor; this package only transports
// complete IPv4 packets through the selected encrypted document carrier.
package mobile

import (
	"fmt"
	"strings"
	"sync"

	"universal-bypass-tool/transport"
	"universal-bypass-tool/transport/mailru"
	"universal-bypass-tool/transport/yandex"
	"universal-bypass-tool/utils"
)

var client = packetClient{}
var packetCond = sync.NewCond(&client.mu)

type packetClient struct {
	mu        sync.Mutex
	running   bool
	transport transport.Transport
	encrypted *transport.EncryptedTransport
	packets   [][]byte
	logs      []string
}

func appendLog(message string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.logs = append(client.logs, message)
	if len(client.logs) > 500 {
		client.logs = append([]string(nil), client.logs[len(client.logs)-500:]...)
	}
}

// Start preserves the existing Android API and defaults to Yandex Volga.
func Start(documentURL, encryptionSecret string) string {
	return StartWithTransport("volga", documentURL, encryptionSecret)
}

// StartWithTransport connects the packet transport. It returns an empty string
// on success and a user-readable error on failure.
func StartWithTransport(carrierName, documentURL, encryptionSecret string) string {
	if strings.TrimSpace(documentURL) == "" {
		return "Ссылка на документ не указана"
	}
	if len(encryptionSecret) < 16 {
		return "Ключ шифрования должен содержать не менее 16 символов"
	}

	client.mu.Lock()
	if client.running {
		client.mu.Unlock()
		return ""
	}
	client.running = true
	client.packets = nil
	client.logs = nil
	client.encrypted = nil
	client.mu.Unlock()

	config := transport.DefaultConfig()
	carrier, encryptionContext, displayName, err := newMobileCarrier(carrierName, documentURL, config)
	if err != nil {
		client.mu.Lock()
		client.running = false
		client.mu.Unlock()
		return err.Error()
	}

	utils.EnableDebug()
	utils.SetLogSink(appendLog)
	appendLog(fmt.Sprintf("[ANDROID] Запуск транспорта %s", displayName))

	encrypted, err := transport.NewEncryptedTransport(
		carrier, encryptionSecret, encryptionContext, false,
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
		client.mu.Lock()
		if !client.running {
			client.mu.Unlock()
			return
		}
		if len(client.packets) >= config.MaxQueueSize {
			client.packets = client.packets[1:]
		}
		client.packets = append(client.packets, packet)
		packetCond.Signal()
		client.mu.Unlock()
	})

	if err := trans.Start(); err != nil {
		appendLog(fmt.Sprintf("[ANDROID] Ошибка запуска: %v", err))
		client.mu.Lock()
		client.running = false
		client.mu.Unlock()
		return err.Error()
	}

	client.mu.Lock()
	client.transport = trans
	client.encrypted = encrypted
	client.mu.Unlock()
	return ""
}

func newMobileCarrier(name, reference string, config transport.TransportConfig) (transport.Transport, string, string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "volga", "yandex-volga":
		return yandex.NewResilientYandexVolgaTransport(reference, config), reference, "Yandex Volga", nil
	case "mailru", "mail.ru", "mailru-docs":
		weblink := mailru.NormalizeWeblink(reference)
		if weblink == "" {
			return nil, "", "", fmt.Errorf("ссылка на публичный документ Mail.ru пуста")
		}
		return mailru.NewDocsTransport(weblink, config), "mailru:" + weblink, "Mail.ru Docs", nil
	default:
		return nil, "", "", fmt.Errorf("неизвестный транспорт %q", name)
	}
}

func Stop() {
	client.mu.Lock()
	trans := client.transport
	client.running = false
	client.transport = nil
	client.encrypted = nil
	client.packets = nil
	packetCond.Broadcast()
	client.mu.Unlock()
	appendLog("[ANDROID] Остановка транспорта")
	if trans != nil {
		_ = trans.Stop()
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

// ReadWait blocks until a packet arrives or the transport is stopped.
func ReadWait() []byte {
	client.mu.Lock()
	defer client.mu.Unlock()

	for client.running && len(client.packets) == 0 {
		packetCond.Wait()
	}
	if len(client.packets) == 0 {
		return nil
	}

	packet := client.packets[0]
	client.packets = client.packets[1:]
	return packet
}

// Read is kept for compatibility with older Android bindings.
func Read() []byte {
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.packets) == 0 {
		return nil
	}
	packet := client.packets[0]
	client.packets = client.packets[1:]
	return packet
}

func ReadLogs() string {
	client.mu.Lock()
	defer client.mu.Unlock()
	logs := strings.Join(client.logs, "\n")
	client.logs = nil
	return logs
}
