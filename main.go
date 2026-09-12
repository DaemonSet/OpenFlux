package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/wlynxg/anet"

	"universal-bypass-tool/socks5"
	"universal-bypass-tool/transport"
	"universal-bypass-tool/transport/yandex"
	"universal-bypass-tool/tunnel"
	"universal-bypass-tool/utils"
)

func main() {
	exitNode := flag.Bool("exit-node", false, "Run as exit node (needs root)")
	clientMode := flag.Bool("client", false, "Run as SOCKS5 client")
	debug := flag.Bool("debug", false, "Enable verbose debug logging")
	socksAddr := flag.String("socks5", ":1080", "SOCKS5 listen address")

	var documentURL string
	flag.StringVar(&documentURL, "url", "", "Yandex document URL; prefer --url-file")
	urlFile := flag.String("url-file", "", "Read the document URL from a file")
	encryptionKeyFile := flag.String(
		"encryption-key-file",
		"",
		"Read the shared transport encryption secret from a file",
	)
	flag.Parse()

	// Exactly one runtime mode is required.
	if *exitNode == *clientMode {
		flag.Usage()
		os.Exit(2)
	}

	if *debug {
		utils.EnableDebug()
	}

	var err error
	documentURL, err = readRequiredOption(documentURL, *urlFile, "document URL")
	if err != nil {
		log.Fatal(err)
	}

	secret, err := readRequiredOption("", *encryptionKeyFile, "encryption key")
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("=== OpenFlux ===")
	log.Printf("Mode: %s", map[bool]string{true: "EXIT NODE", false: "CLIENT"}[*exitNode])
	log.Printf("Carrier: Yandex Volga")

	config := transport.DefaultConfig()

	carrier := yandex.NewResilientYandexVolgaTransport(documentURL, config)

	encrypted, err := transport.NewEncryptedTransport(
		carrier,
		secret,
		documentURL,
		*exitNode,
	)
	if err != nil {
		log.Fatalf("Configure encrypted Volga transport: %v", err)
	}

	compressed := transport.NewCompressedTransport(encrypted)

	multiplexed, err := transport.NewMultiplexTransport(compressed, *exitNode)
	if err != nil {
		log.Fatalf("Configure multi-client transport: %v", err)
	}

	if !*exitNode {
		log.Printf("Multi-client ID: %s", multiplexed.ClientID())
	}

	// DNS control messages use the same encrypted Volga carrier as data packets.
	var trans transport.Transport = multiplexed
	trans = transport.NewDNSMuxTransport(trans, *exitNode)

	if err := trans.Start(); err != nil {
		log.Fatalf("Start Volga transport: %v", err)
	}

	if *exitNode {
		go superviseExitLiveness(encrypted, carrier)
	}

	tun := tunnel.NewTCPTunnel(trans, *exitNode)

	if *exitNode {
		log.Printf("Running as EXIT NODE")
		select {}
	}

	log.Printf("Running as CLIENT (SOCKS5 on %s)", *socksAddr)
	server := socks5.NewSOCKS5Server(*socksAddr, tun)
	log.Fatal(server.Start())
}

const (
	exitLivenessCheckInterval = 10 * time.Second
	exitLivenessFailureWindow = 90 * time.Second
)

func superviseExitLiveness(
	encrypted *transport.EncryptedTransport,
	carrier *yandex.ResilientYandexVolgaTransport,
) {
	ticker := time.NewTicker(exitLivenessCheckInterval)
	defer ticker.Stop()

	var lastSequence int64
	var lastHeartbeat time.Time
	armed := false

	for now := range ticker.C {
		sequence := encrypted.PeerPingSequence()

		if sequence != lastSequence {
			lastSequence = sequence
			lastHeartbeat = now
			armed = true
			continue
		}

		if !armed || lastHeartbeat.IsZero() {
			continue
		}

		if now.Sub(lastHeartbeat) < exitLivenessFailureWindow {
			continue
		}

		log.Printf(
			"Exit liveness timeout: no encrypted client heartbeat for %s; rebuilding Volga session",
			now.Sub(lastHeartbeat).Round(time.Second),
		)

		carrier.ForceReconnect("exit end-to-end encrypted client heartbeat timeout")

		armed = false
		lastHeartbeat = time.Time{}
	}
}

func readRequiredOption(value, filename, label string) (string, error) {
	if value != "" && filename != "" {
		return "", fmt.Errorf("use only one of the inline or file options for %s", label)
	}

	if filename != "" {
		data, err := os.ReadFile(filename)
		if err != nil {
			return "", fmt.Errorf("read %s file: %w", label, err)
		}
		value = string(data)
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", label)
	}

	return value, nil
}
