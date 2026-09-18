package yandex

import (
	"bytes"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
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

func TestVolgaDocumentCookieSnapshotForRelay(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}

	docURL, err := url.Parse("https://volga.yandex.ru/document/example")
	if err != nil {
		t.Fatal(err)
	}

	jar.SetCookies(docURL, []*http.Cookie{
		{
			Name:  "volga-session",
			Value: "test-value",
			Path:  "/document/",
		},
	})

	snapshot := volgaCookieHeader(jar, docURL.String())
	if !strings.Contains(snapshot, "volga-session=test-value") {
		t.Fatalf("document cookie missing from snapshot: %q", snapshot)
	}

	relayURL := "https://volga.yandex.ru/session/main/example/relay"
	if got := volgaCookieHeader(jar, relayURL); got != "" {
		t.Fatalf("path-scoped cookie unexpectedly available to relay URL: %q", got)
	}
}

func TestVolgaRelayAuthFailureCallbackRunsOnce(t *testing.T) {
	calls := 0
	r := &volgaRelay{
		onAuthFailure: func() {
			calls++
		},
	}

	r.noteSendError(&volgaRelayHTTPError{StatusCode: http.StatusUnauthorized})
	r.noteSendError(&volgaRelayHTTPError{StatusCode: http.StatusForbidden})
	r.noteSendError(&volgaRelayHTTPError{StatusCode: http.StatusInternalServerError})

	if calls != 1 {
		t.Fatalf("auth failure callback calls=%d want=1", calls)
	}
}

func TestVolgaRelayAuthErrorClassification(t *testing.T) {
	if !isVolgaRelayAuthError(&volgaRelayHTTPError{StatusCode: http.StatusUnauthorized}) {
		t.Fatal("401 must be classified as auth failure")
	}
	if !isVolgaRelayAuthError(&volgaRelayHTTPError{StatusCode: http.StatusForbidden}) {
		t.Fatal("403 must be classified as auth failure")
	}
	if isVolgaRelayAuthError(&volgaRelayHTTPError{StatusCode: http.StatusInternalServerError}) {
		t.Fatal("500 must not be classified as auth failure")
	}
}
