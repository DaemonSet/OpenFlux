package yandex

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

func TestVolgaRelayRepeatedFailuresTriggerTransportFailure(t *testing.T) {
	calls := 0
	r := &volgaRelay{
		cfg: VolgaConfig{RelayFailureThreshold: 3},
		onTransportFailure: func(error) {
			calls++
		},
	}

	err := errors.New("relay unavailable")
	r.noteSendError(err)
	r.noteSendError(err)
	if calls != 0 {
		t.Fatalf("transport failure callback calls=%d before threshold", calls)
	}
	r.noteSendError(err)
	r.noteSendError(err)
	if calls != 1 {
		t.Fatalf("transport failure callback calls=%d want=1", calls)
	}
}

func TestVolgaRelaySuccessResetsFailureStreak(t *testing.T) {
	calls := 0
	r := &volgaRelay{
		cfg: VolgaConfig{RelayFailureThreshold: 3},
		onTransportFailure: func(error) {
			calls++
		},
	}

	err := errors.New("relay unavailable")
	r.noteSendError(err)
	r.noteSendError(err)
	r.noteSendSuccess()
	r.noteSendError(err)
	r.noteSendError(err)
	if calls != 0 {
		t.Fatalf("transport failure callback calls=%d after reset", calls)
	}
	r.noteSendError(err)
	if calls != 1 {
		t.Fatalf("transport failure callback calls=%d want=1", calls)
	}
}

func TestVolgaWSStopInterruptsBlockedRead(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(rw, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &volgaWS{ctx: ctx, cancel: cancel}
	if !w.setActiveConn(conn) {
		t.Fatal("failed to register active websocket")
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		_, _, _ = conn.ReadMessage()
	}()

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop remained blocked on websocket ReadMessage")
	}
}

func TestVolgaSafeURLStripsSensitiveQuery(t *testing.T) {
	raw := "https://volga.yandex.ru/document/?token=secret-token&sign=secret-sign#fragment"
	got := volgaSafeURL(raw)
	if got != "https://volga.yandex.ru/document/" {
		t.Fatalf("safe URL=%q", got)
	}
	if strings.Contains(got, "secret-token") || strings.Contains(got, "secret-sign") {
		t.Fatalf("safe URL leaked query data: %q", got)
	}
}

func TestVolgaHTTPErrorCauseDropsRequestURL(t *testing.T) {
	err := &url.Error{
		Op:  "Get",
		URL: "https://volga.yandex.ru/document/?token=secret-token",
		Err: errors.New("network unavailable"),
	}
	got := volgaHTTPErrorCause(err)
	if got == nil {
		t.Fatal("expected error cause")
	}
	if got.Error() != "network unavailable" {
		t.Fatalf("cause=%q", got.Error())
	}
	if strings.Contains(got.Error(), "secret-token") {
		t.Fatalf("error cause leaked request URL: %q", got.Error())
	}
}

func TestVolgaHTTPClientUsesBoundedTimeouts(t *testing.T) {
	timeouts := volgaHTTPTimeouts{
		Dial:           2 * time.Second,
		TLSHandshake:   3 * time.Second,
		ResponseHeader: 4 * time.Second,
		Request:        5 * time.Second,
	}
	client, err := newVolgaHTTPClientWithTimeouts(timeouts)
	if err != nil {
		t.Fatal(err)
	}
	if client.Timeout != timeouts.Request {
		t.Fatalf("client timeout=%s want=%s", client.Timeout, timeouts.Request)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type=%T want *http.Transport", client.Transport)
	}
	if tr.TLSHandshakeTimeout != timeouts.TLSHandshake {
		t.Fatalf("TLS handshake timeout=%s want=%s", tr.TLSHandshakeTimeout, timeouts.TLSHandshake)
	}
	if tr.ResponseHeaderTimeout != timeouts.ResponseHeader {
		t.Fatalf("response header timeout=%s want=%s", tr.ResponseHeaderTimeout, timeouts.ResponseHeader)
	}
	if tr.DialContext == nil {
		t.Fatal("HTTP transport has no bounded DialContext")
	}
}

func TestVolgaHTTPResponseHeaderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		time.Sleep(200 * time.Millisecond)
		rw.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := newVolgaHTTPClientWithTimeouts(volgaHTTPTimeouts{
		Dial:           time.Second,
		TLSHandshake:   time.Second,
		ResponseHeader: 40 * time.Millisecond,
		Request:        time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	resp, err := client.Get(server.URL)
	elapsed := time.Since(started)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("request unexpectedly succeeded despite response-header timeout")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("response-header timeout took %s; want <500ms", elapsed)
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
