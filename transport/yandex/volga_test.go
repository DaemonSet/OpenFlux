package yandex

import (
	"bytes"
	"testing"
)

func TestVolgaBatchRoundTrip(t *testing.T) {
	in := [][]byte{
		{0x45, 0x00, 0x00, 0x28},
		bytes.Repeat([]byte{0xab}, 1500),
		[]byte("openflux-volga"),
	}

	encoded, total, err := volgaEncodeBatch(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	wantTotal := 0
	for _, packet := range in {
		wantTotal += len(packet)
	}
	if total != wantTotal {
		t.Fatalf("total=%d want=%d", total, wantTotal)
	}

	out, ok := volgaDecodeBatch(encoded)
	if !ok {
		t.Fatal("decode rejected our frame")
	}
	if len(out) != len(in) {
		t.Fatalf("packet count=%d want=%d", len(out), len(in))
	}
	for i := range in {
		if !bytes.Equal(out[i], in[i]) {
			t.Fatalf("packet %d differs", i)
		}
	}
}

func TestVolgaBatchRejectsForeignBase64(t *testing.T) {
	if packets, ok := volgaDecodeBatch("aGVsbG8="); ok || packets != nil {
		t.Fatalf("foreign base64 accepted: %#v", packets)
	}
}

func TestVolgaParseClientConfig(t *testing.T) {
	html := []byte(`<html><script id="client-config">{"officeActionData":{"action_url":"https://example.invalid/a","access_token":"x"}}</script></html>`)
	cfg, err := volgaParseClientConfig(html)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg["officeActionData"] == nil {
		t.Fatal("officeActionData missing after parse")
	}
}
