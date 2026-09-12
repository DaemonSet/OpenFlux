package socks5

import (
	"net"
	"testing"
	"time"
)

type unusedDialer struct{}

func (unusedDialer) DialTCP(string) (net.Conn, error) { return nil, nil }

func TestStopClosesListener(t *testing.T) {
	server := NewSOCKS5Server("127.0.0.1:0", unusedDialer{})
	result := make(chan error, 1)
	go func() { result <- server.Start() }()

	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		ready := server.listener != nil
		server.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener did not start")
		}
		time.Sleep(time.Millisecond)
	}

	if err := server.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Start() after Stop() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start() did not return after Stop()")
	}
}

func TestStopBeforeStart(t *testing.T) {
	server := NewSOCKS5Server("127.0.0.1:0", unusedDialer{})
	if err := server.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := server.Start(); err == nil {
		t.Fatal("Start() after Stop() unexpectedly succeeded")
	}
}
