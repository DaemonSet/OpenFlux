package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	_ "github.com/wlynxg/anet"

	"universal-bypass-tool/socks5"
	"universal-bypass-tool/transport"
	"universal-bypass-tool/transport/mailru"
	"universal-bypass-tool/transport/yandex"
	"universal-bypass-tool/tunnel"
	"universal-bypass-tool/utils"
)

func main() {
	exitNode := flag.Bool("exit-node", false, "Run as exit node (needs root)")
	clientMode := flag.Bool("client", false, "Run as SOCKS5 client")
	debug := flag.Bool("debug", false, "Enable verbose debug logging")
	socksAddr := flag.String("socks5", ":1080", "SOCKS5 listen address")
	carrierName := flag.String("transport", "volga", "Carrier transport: volga or mailru")

	var documentURL string
	flag.StringVar(&documentURL, "url", "", "Public document URL; prefer --url-file")
	urlFile := flag.String("url-file", "", "Read the public document URL from a file")
	encryptionKeyFile := flag.String(
		"encryption-key-file",
		"",
		"Read the shared transport encryption secret from a file",
	)
	dnsListen := flag.String(
		"dns-listen",
		"",
		"Local DNS-over-TCP listener for DNSMux client requests",
	)
	dnsServer := flag.String(
		"dns-server",
		"94.140.14.14",
		"Primary DNS server queried by the exit node",
	)
	dnsFallback := flag.String(
		"dns-fallback",
		"94.140.15.15",
		"Fallback DNS server queried by the exit node",
	)
	flag.Parse()

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

	config := transport.DefaultConfig()
	carrier, encryptionContext, carrierDisplay, err := newCarrier(*carrierName, documentURL, config)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("=== OpenFlux ===")
	log.Printf("Mode: %s", map[bool]string{true: "EXIT NODE", false: "CLIENT"}[*exitNode])
	log.Printf("Carrier: %s", carrierDisplay)

	encrypted, err := transport.NewEncryptedTransport(
		carrier,
		secret,
		encryptionContext,
		*exitNode,
	)
	if err != nil {
		log.Fatalf("Configure encrypted %s transport: %v", carrierDisplay, err)
	}

	compressed := transport.NewCompressedTransport(encrypted)

	multiplexed, err := transport.NewMultiplexTransport(compressed, *exitNode)
	if err != nil {
		log.Fatalf("Configure multi-client transport: %v", err)
	}

	if !*exitNode {
		log.Printf("Multi-client ID: %s", multiplexed.ClientID())
	}

	dnsMux := transport.NewDNSMuxTransport(multiplexed, *exitNode)

	var trans transport.Transport = dnsMux

	if err := trans.Start(); err != nil {
		log.Fatalf("Start %s transport: %v", carrierDisplay, err)
	}

	if !*exitNode && *dnsListen != "" {
		dnsListener, err := startDNSTCPProxy(
			*dnsListen,
			dnsMux,
			*dnsServer,
			*dnsFallback,
		)
		if err != nil {
			log.Fatalf("Start DNSMux TCP proxy: %v", err)
		}
		defer dnsListener.Close()

		log.Printf(
			"DNSMux TCP proxy listening on %s",
			dnsListener.Addr(),
		)
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

func newCarrier(name, reference string, config transport.TransportConfig) (transport.Transport, string, string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "volga", "yandex-volga":
		return yandex.NewResilientYandexVolgaTransport(reference, config), reference, "Yandex Volga", nil
	case "mailru", "mail.ru", "mailru-docs":
		weblink := mailru.NormalizeWeblink(reference)
		if weblink == "" {
			return nil, "", "", fmt.Errorf("Mail.ru public document reference is empty")
		}
		return mailru.NewDocsTransport(weblink, config), "mailru:" + weblink, "Mail.ru Docs", nil
	default:
		return nil, "", "", fmt.Errorf("unknown transport %q (want volga or mailru)", name)
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
