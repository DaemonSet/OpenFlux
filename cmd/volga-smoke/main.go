package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"universal-bypass-tool/transport"
	"universal-bypass-tool/transport/yandex"
	"universal-bypass-tool/utils"
)

func main() {
	mode := flag.String("mode", "", "echo or ping")
	urlFile := flag.String("url-file", "", "file containing Yandex document URL")
	debug := flag.Bool("debug", false, "enable debug logs")
	flag.Parse()

	if *debug {
		utils.EnableDebug()
	}

	if *mode != "echo" && *mode != "ping" {
		log.Fatal("-mode must be echo or ping")
	}
	if *urlFile == "" {
		log.Fatal("-url-file is required")
	}

	b, err := os.ReadFile(*urlFile)
	if err != nil {
		log.Fatalf("read URL file: %v", err)
	}
	docURL := strings.TrimSpace(string(b))
	if docURL == "" {
		log.Fatal("empty document URL")
	}

	t := yandex.NewYandexVolgaTransport(docURL, transport.DefaultConfig())

	switch *mode {
	case "echo":
		t.Receive(func(data []byte) {
			s := string(data)
			log.Printf("RX: %q", s)

			if strings.HasPrefix(s, "PING ") {
				reply := "PONG " + strings.TrimPrefix(s, "PING ")
				if err := t.Send([]byte(reply)); err != nil {
					log.Printf("TX error: %v", err)
					return
				}
				log.Printf("TX: %q", reply)
			}
		})

	case "ping":
		done := make(chan struct{}, 1)
		nonce := fmt.Sprintf("%d", time.Now().UnixNano())
		expected := "PONG " + nonce

		t.Receive(func(data []byte) {
			s := string(data)
			log.Printf("RX: %q", s)
			if s == expected {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		})

		if err := t.Start(); err != nil {
			log.Fatalf("start: %v", err)
		}
		defer t.Stop()

		log.Printf("VOLGA READY")

		msg := "PING " + nonce
		log.Printf("TX: %q", msg)

		if err := t.Send([]byte(msg)); err != nil {
			log.Fatalf("send: %v", err)
		}

		select {
		case <-done:
			log.Printf("SUCCESS: round-trip Volga works")
			return
		case <-time.After(30 * time.Second):
			log.Fatal("TIMEOUT: no PONG received")
		}
	}

	if err := t.Start(); err != nil {
		log.Fatalf("start: %v", err)
	}
	defer t.Stop()

	log.Printf("VOLGA READY — echo mode")

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
}
