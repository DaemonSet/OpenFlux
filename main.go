package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	_ "github.com/wlynxg/anet"
	"universal-bypass-tool/socks5"
	"universal-bypass-tool/transport"
	"universal-bypass-tool/transport/oneme"
	"universal-bypass-tool/transport/yandex"
	"universal-bypass-tool/tunnel"
	"universal-bypass-tool/utils"
)

var (
	globalDocUrl string
	maxToken     string
	maxUid       string
)

func main() {
	//os.Setenv("GODEBUG", "netdns=go")
	fmt.Print("written by p1neappleXpress\n")

	exitNode := flag.Bool("exit-node", false, "Run as exit node (needs root)")
	client := flag.Bool("client", false, "Run as client")
	debug := flag.Bool("debug", false, "Enable verbose debug logging")
	socksAddr := flag.String("socks5", ":1080", "SOCKS5 address")
	transportType := flag.String("transport", "yandex", "Transport type (yandex, yandex-volga, oneme)")
	flag.StringVar(&globalDocUrl, "url", "", "Document URL. Required for Yandex.Docs transport")
	urlFile := flag.String("url-file", "", "Read the document URL from a file")
	encryptionKeyFile := flag.String("encryption-key-file", "", "Read the shared encryption secret from a file")
	maxTokenFile := flag.String("max-token-file", "", "Read the MAX Web token from a file")
	flag.StringVar(&maxToken, "maxToken", "", "MAX Web token. If u use MAX transport")
	flag.StringVar(&maxUid, "maxUid", "", "MAX call user id. If u use MAX transport")
	flag.Parse()

	if !*exitNode && !*client {
		flag.Usage()
		os.Exit(1)
	}

	if *debug {
		utils.EnableDebug()
	}

	log.Printf("=== Universal Bypass Tool ===")
	log.Printf("Mode: %s", map[bool]string{true: "EXIT NODE", false: "CLIENT"}[*exitNode])
	log.Printf("Transport: %s", *transportType)

	config := transport.DefaultConfig()
	var trans transport.Transport

	switch *transportType {
	case "yandex":
		var err error
		globalDocUrl, err = readRequiredOption(globalDocUrl, *urlFile, "document URL")
		if err != nil {
			log.Fatal(err)
		}
		secret, err := readRequiredOption("", *encryptionKeyFile, "encryption key")
		if err != nil {
			log.Fatal(err)
		}
		encrypted, err := transport.NewEncryptedTransport(
			yandex.NewYandexDocsTransport(globalDocUrl, config), secret, globalDocUrl, *exitNode,
		)
		if err != nil {
			log.Fatalf("Configure encrypted transport: %v", err)
		}
		trans = transport.NewCompressedTransport(encrypted)
	case "yandex-volga":
		var err error
		globalDocUrl, err = readRequiredOption(globalDocUrl, *urlFile, "document URL")
		if err != nil {
			log.Fatal(err)
		}
		secret, err := readRequiredOption("", *encryptionKeyFile, "encryption key")
		if err != nil {
			log.Fatal(err)
		}
		encrypted, err := transport.NewEncryptedTransport(
			yandex.NewResilientYandexVolgaTransport(globalDocUrl, config), secret, globalDocUrl, *exitNode,
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
		trans = multiplexed
	case "oneme":
		var err error
		maxToken, err = readRequiredOption(maxToken, *maxTokenFile, "MAX token")
		if err != nil {
			log.Fatal(err)
		}
		uidint, err := strconv.ParseInt(maxUid, 10, 64)
		if !*exitNode && (err != nil || uidint <= 0) {
			log.Fatalf("Valid --maxUid is required for MAX client mode")
		}
		trans = transport.NewCompressedTransport(oneme.NewOneMeTransport(*exitNode, maxToken, uidint, config))
	default:
		log.Fatalf("Unknown transport type: %s", *transportType)
	}

	// DNS control messages share the same carrier as normal OpenFlux packets.
	trans = transport.NewDNSMuxTransport(trans, *exitNode)

	if err := trans.Start(); err != nil {
		log.Fatalf("Failed to start transport: %v", err)
	}

	tun := tunnel.NewTCPTunnel(trans, *exitNode)

	if *exitNode {
		log.Printf("Running as EXIT NODE (needs root for raw socket)")
		log.Printf("! Run: sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP")
		select {}
	} else {
		log.Printf("Running as CLIENT (SOCKS5 on %s)", *socksAddr)
		socks5Server := socks5.NewSOCKS5Server(*socksAddr, tun)
		log.Fatal(socks5Server.Start())
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
