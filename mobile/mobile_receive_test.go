package mobile

import (
	"bytes"
	"testing"
	"time"
)

func TestReadPacketBlocksUntilPacket(t *testing.T) {
	queue := make(chan []byte, 1)
	stop := make(chan struct{})
	result := make(chan []byte, 1)

	go func() {
		result <- readPacket(queue, stop)
	}()

	select {
	case <-result:
		t.Fatal("readPacket returned while queue was empty")
	case <-time.After(30 * time.Millisecond):
	}

	want := []byte{1, 2, 3, 4}
	queue <- want

	select {
	case got := <-result:
		if !bytes.Equal(got, want) {
			t.Fatalf("readPacket = %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("readPacket did not wake when packet arrived")
	}
}

func TestReadPacketStopsBlockedReader(t *testing.T) {
	queue := make(chan []byte, 1)
	stop := make(chan struct{})
	result := make(chan []byte, 1)

	go func() {
		result <- readPacket(queue, stop)
	}()

	select {
	case <-result:
		t.Fatal("readPacket returned before stop")
	case <-time.After(30 * time.Millisecond):
	}

	close(stop)

	select {
	case got := <-result:
		if got != nil {
			t.Fatalf("readPacket after stop = %v, want nil", got)
		}
	case <-time.After(time.Second):
		t.Fatal("readPacket did not wake after stop")
	}
}

func TestEnqueuePacketDropsOldestWhenFull(t *testing.T) {
	queue := make(chan []byte, 2)
	stop := make(chan struct{})

	enqueuePacket(queue, stop, []byte{1})
	enqueuePacket(queue, stop, []byte{2})
	enqueuePacket(queue, stop, []byte{3})

	first := <-queue
	second := <-queue

	if !bytes.Equal(first, []byte{2}) || !bytes.Equal(second, []byte{3}) {
		t.Fatalf("queue contains %v, %v; want [2], [3]", first, second)
	}
}
