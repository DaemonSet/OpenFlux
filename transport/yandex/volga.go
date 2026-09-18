package yandex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"universal-bypass-tool/transport"
	"universal-bypass-tool/utils"
)

// Volga transport is a Yandex Docs carrier for the modern "volga" editor.
//
// The public document page provides a short-lived access token and action URL.
// The action URL redirects to volga.yandex.ru and yields the relay token,
// request-path and Xiva subscription parameters. Outbound OpenFlux frames are
// sent through Volga's /relay endpoint; inbound frames are received from the
// Yandex push websocket.
//
// This transport is intentionally separate from the legacy Yandex Docs
// implementation so both can be tested independently.

type VolgaConfig struct {
	MaxIdleConnsPerHost   int
	MaxIdleConns          int
	IdleConnTimeout       time.Duration
	RelayTimeout          time.Duration
	RelayFailureThreshold int

	WorkerCount int
	QueueSize   int

	BatchSize     int
	BatchTimeout  time.Duration
	BatchMaxBytes int
	MaxPacketSize int

	ReconnectMinDelay   time.Duration
	ReconnectMaxDelay   time.Duration
	ReconnectMultiplier float64

	WSHandshakeTimeout time.Duration
	WSReadTimeout      time.Duration
	StartTimeout       time.Duration
}

func DefaultVolgaConfig() VolgaConfig {
	return VolgaConfig{
		MaxIdleConnsPerHost:   64,
		MaxIdleConns:          128,
		IdleConnTimeout:       90 * time.Second,
		RelayTimeout:          15 * time.Second,
		RelayFailureThreshold: 3,

		WorkerCount: 8,
		QueueSize:   4096,

		BatchSize:     16,
		BatchTimeout:  3 * time.Millisecond,
		BatchMaxBytes: 256 * 1024,
		MaxPacketSize: 60 * 1024,

		ReconnectMinDelay:   500 * time.Millisecond,
		ReconnectMaxDelay:   20 * time.Second,
		ReconnectMultiplier: 1.7,

		WSHandshakeTimeout: 10 * time.Second,
		WSReadTimeout:      90 * time.Second,
		StartTimeout:       15 * time.Second,
	}
}

const (
	volgaUserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:153.0) Gecko/20100101 Firefox/153.0"
	volgaWireMagic = "OFV1"
)

var volgaClientConfigRE = regexp.MustCompile(`<script[^>]*id=["']client-config["'][^>]*>(.*?)</script>`)

type volgaStats struct {
	packetsSent    atomic.Uint64
	packetsRecv    atomic.Uint64
	bytesSent      atomic.Uint64
	bytesRecv      atomic.Uint64
	httpSent       atomic.Uint64
	httpFailed     atomic.Uint64
	wsReconnects   atomic.Uint64
	queueDrops     atomic.Uint64
	batchesSent    atomic.Uint64
	packetsBatched atomic.Uint64
}

type volgaAuth struct {
	client *http.Client

	finalURL string
	origin   string

	accessToken       string
	token             string
	requestPath       string
	resourceURL       string
	docID             string
	relayCookieHeader string

	userID    int64
	userIDStr string
	sign      string
	ts        string
	sessionID string
}

type volgaHTTPTimeouts struct {
	Dial           time.Duration
	TLSHandshake   time.Duration
	ResponseHeader time.Duration
	Request        time.Duration
}

var defaultVolgaHTTPTimeouts = volgaHTTPTimeouts{
	Dial:           5 * time.Second,
	TLSHandshake:   5 * time.Second,
	ResponseHeader: 8 * time.Second,
	Request:        12 * time.Second,
}

func newVolgaHTTPClient() (*http.Client, error) {
	return newVolgaHTTPClientWithTimeouts(defaultVolgaHTTPTimeouts)
}

func newVolgaHTTPClientWithTimeouts(timeouts volgaHTTPTimeouts) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{
		Timeout:   timeouts.Dial,
		KeepAlive: 30 * time.Second,
	}
	return &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   timeouts.TLSHandshake,
			ResponseHeaderTimeout: timeouts.ResponseHeader,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		Timeout: timeouts.Request,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func volgaAuthorize(docURL string) (*volgaAuth, error) {
	client, err := newVolgaHTTPClient()
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	finalURL, body, err := volgaFetchDocumentPage(client, docURL)
	if err != nil {
		return nil, err
	}

	cfg, err := volgaParseClientConfig(body)
	if err != nil {
		return nil, err
	}

	office, _ := cfg["officeActionData"].(map[string]interface{})
	if office == nil {
		return nil, fmt.Errorf("officeActionData missing (client-config keys: %v)", volgaMapKeys(cfg))
	}
	editor, _ := cfg["editorParams"].(map[string]interface{})

	actionURL := volgaString(office, "action_url")
	accessToken := volgaString(office, "access_token")
	if actionURL == "" {
		return nil, fmt.Errorf("officeActionData.action_url missing (keys: %v)", volgaMapKeys(office))
	}
	if accessToken == "" {
		return nil, errors.New("officeActionData.access_token missing")
	}

	a := &volgaAuth{
		client:      client,
		finalURL:    finalURL,
		origin:      volgaOrigin(finalURL),
		accessToken: accessToken,
		resourceURL: volgaString(office, "resource_url"),
		docID:       volgaString(editor, "idDoc"),
	}

	form := url.Values{}
	form.Set("access_token", accessToken)
	form.Set("access_token_ttl", volgaTTL(office["access_token_ttl"]))

	req, err := http.NewRequest(http.MethodPost, actionURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create auth request: %w", err)
	}
	volgaSetBrowserHeaders(req, a.origin, finalURL)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	requestStarted := time.Now()
	utils.Debugf("[VOLGA] POST auth/initial begin %s", volgaSafeURL(actionURL))
	resp, err := client.Do(req)
	requestElapsed := time.Since(requestStarted).Round(time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("POST auth/initial after %s: %w", requestElapsed, volgaHTTPErrorCause(err))
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	utils.Debugf("[VOLGA] POST auth/initial -> %d in %s", resp.StatusCode, requestElapsed)

	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("auth/initial status %d; expected redirect", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return nil, errors.New("auth/initial redirect has no Location")
	}

	resolvedLocation, err := volgaResolveURL(actionURL, location)
	if err != nil {
		return nil, fmt.Errorf("resolve auth redirect: %w", err)
	}
	if strings.Contains(resolvedLocation, "/document/error/") {
		return nil, errors.New("Volga auth returned document/error")
	}

	loc, err := url.Parse(resolvedLocation)
	if err != nil {
		return nil, fmt.Errorf("parse auth Location: %w", err)
	}
	query := loc.Query()
	a.token = query.Get("token")
	a.requestPath = query.Get("request-path")

	jsonText := query.Get("json")
	if jsonText == "" {
		return nil, fmt.Errorf("auth redirect missing json payload (token=%v request-path=%v)", a.token != "", a.requestPath != "")
	}

	var bootstrap map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(jsonText))
	dec.UseNumber()
	if err := dec.Decode(&bootstrap); err != nil {
		return nil, fmt.Errorf("parse auth redirect json: %w", err)
	}

	a.sessionID = volgaString(bootstrap, "sessionId")
	a.userID = volgaInt64(bootstrap, "userId")
	if xiva, ok := bootstrap["xiva"].(map[string]interface{}); ok {
		a.userIDStr = volgaString(xiva, "user")
		a.sign = volgaString(xiva, "sign")
		a.ts = volgaString(xiva, "ts")
	}

	// Visit the redirected document once so the cookie jar sees the same flow
	// as a normal browser before relay/Xiva connections start.
	getReq, err := http.NewRequest(http.MethodGet, resolvedLocation, nil)
	if err == nil {
		volgaSetBrowserHeaders(getReq, a.origin, actionURL)
		bootstrapStarted := time.Now()
		utils.Debugf("[VOLGA] bootstrap GET begin %s", volgaSafeURL(resolvedLocation))
		getResp, getErr := client.Do(getReq)
		bootstrapElapsed := time.Since(bootstrapStarted).Round(time.Millisecond)
		if getErr == nil {
			io.Copy(io.Discard, getResp.Body)
			getResp.Body.Close()
			utils.Debugf("[VOLGA] bootstrap GET -> %d in %s", getResp.StatusCode, bootstrapElapsed)
		} else {
			utils.Debugf("[VOLGA] bootstrap GET failed after %s: %v", bootstrapElapsed, volgaHTTPErrorCause(getErr))
		}
	}

	// Some Volga cookies are scoped to the bootstrap document path.
	// Snapshot them here so relay requests can send the same authenticated
	// browser session even when the cookie jar would omit them for /relay.
	a.relayCookieHeader = volgaCookieHeader(client.Jar, resolvedLocation)

	if a.token == "" || a.requestPath == "" || a.userIDStr == "" || a.sign == "" || a.sessionID == "" {
		return nil, fmt.Errorf(
			"incomplete Volga auth: token=%v requestPath=%v user=%v sign=%v session=%v",
			a.token != "", a.requestPath != "", a.userIDStr != "", a.sign != "", a.sessionID != "",
		)
	}

	utils.Debugf("[VOLGA] auth OK user=%d xiva=%s request-path=%s", a.userID, a.userIDStr, a.requestPath)
	return a, nil
}

func volgaFetchDocumentPage(client *http.Client, docURL string) (string, []byte, error) {
	current := strings.TrimSpace(docURL)
	if current == "" {
		return "", nil, errors.New("empty document URL")
	}

	for hop := 0; hop < 10; hop++ {
		req, err := http.NewRequest(http.MethodGet, current, nil)
		if err != nil {
			return "", nil, fmt.Errorf("create document request: %w", err)
		}
		volgaSetBrowserHeaders(req, volgaOrigin(current), docURL)

		requestStarted := time.Now()
		utils.Debugf("[VOLGA] GET begin %s", volgaSafeURL(current))
		resp, err := client.Do(req)
		requestElapsed := time.Since(requestStarted).Round(time.Millisecond)
		if err != nil {
			return "", nil, fmt.Errorf("GET %s after %s: %w", volgaSafeURL(current), requestElapsed, volgaHTTPErrorCause(err))
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if readErr != nil {
			return "", nil, fmt.Errorf("read %s after %s: %w", volgaSafeURL(current), requestElapsed, readErr)
		}

		utils.Debugf("[VOLGA] GET %s -> %d (%d bytes) in %s", volgaSafeURL(current), resp.StatusCode, len(body), requestElapsed)

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			if location == "" {
				return "", nil, fmt.Errorf("redirect without Location from %s", current)
			}
			current, err = volgaResolveURL(current, location)
			if err != nil {
				return "", nil, err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", nil, fmt.Errorf("document page returned HTTP %d", resp.StatusCode)
		}
		return current, body, nil
	}

	return "", nil, errors.New("too many document redirects")
}

func volgaParseClientConfig(body []byte) (map[string]interface{}, error) {
	match := volgaClientConfigRE.FindSubmatch(body)
	if len(match) < 2 {
		return nil, errors.New("client-config not found in document page")
	}

	var cfg map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(match[1]))
	dec.UseNumber()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse client-config: %w", err)
	}
	return cfg, nil
}

func volgaSetBrowserHeaders(req *http.Request, origin, referer string) {
	req.Header.Set("User-Agent", volgaUserAgent)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.7")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
}

func volgaResolveURL(baseURL, location string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

func volgaSafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<invalid-url>"
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

func volgaHTTPErrorCause(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

func volgaOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "https://disk.yandex.ru"
	}
	return u.Scheme + "://" + u.Host
}

func volgaString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	default:
		return ""
	}
}

func volgaInt64(m map[string]interface{}, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case json.Number:
		i, _ := v.Int64()
		return i
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		i, _ := strconv.ParseInt(v, 10, 64)
		return i
	default:
		return 0
	}
}

func volgaTTL(v interface{}) string {
	switch x := v.(type) {
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case string:
		return x
	default:
		return "0"
	}
}

func volgaMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func volgaCookieHeader(jar http.CookieJar, rawURL string) string {
	if jar == nil {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	cookies := jar.Cookies(u)
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

type volgaRelayHTTPError struct {
	StatusCode int
}

func (e *volgaRelayHTTPError) Error() string {
	return fmt.Sprintf("relay POST returned HTTP %d", e.StatusCode)
}

func isVolgaRelayAuthError(err error) bool {
	var httpErr *volgaRelayHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.StatusCode == http.StatusUnauthorized ||
		httpErr.StatusCode == http.StatusForbidden
}

type volgaRelay struct {
	auth  *volgaAuth
	cfg   VolgaConfig
	stats *volgaStats

	client *http.Client

	onAuthFailure   func()
	authFailureOnce sync.Once

	onTransportFailure   func(error)
	transportFailureOnce sync.Once
	healthMu             sync.Mutex
	consecutiveFailures  int

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	queue   chan []byte
	batches chan [][]byte

	bundleID atomic.Uint64
	seq      atomic.Uint64
	localID  atomic.Uint64

	frontierMu sync.RWMutex
	frontier   string
}

func newVolgaRelay(
	auth *volgaAuth,
	cfg VolgaConfig,
	stats *volgaStats,
	onAuthFailure func(),
	onTransportFailure func(error),
) *volgaRelay {
	ctx, cancel := context.WithCancel(context.Background())
	tr := &http.Transport{
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:     cfg.IdleConnTimeout,
		DisableCompression:  true,
		ForceAttemptHTTP2:   true,
	}
	return &volgaRelay{
		auth:               auth,
		cfg:                cfg,
		stats:              stats,
		onAuthFailure:      onAuthFailure,
		onTransportFailure: onTransportFailure,
		client: &http.Client{
			Transport: tr,
			Timeout:   cfg.RelayTimeout,
			Jar:       auth.client.Jar,
		},
		ctx:     ctx,
		cancel:  cancel,
		queue:   make(chan []byte, cfg.QueueSize),
		batches: make(chan [][]byte, cfg.WorkerCount*2),
	}
}

func (r *volgaRelay) Start() {
	r.wg.Add(1)
	go r.batchLoop()

	for i := 0; i < r.cfg.WorkerCount; i++ {
		r.wg.Add(1)
		go r.worker(i)
	}
	utils.Debugf("[VOLGA] relay started workers=%d queue=%d batch=%d/%dB", r.cfg.WorkerCount, r.cfg.QueueSize, r.cfg.BatchSize, r.cfg.BatchMaxBytes)
}

func (r *volgaRelay) Stop() {
	r.cancel()
	r.wg.Wait()
}

func (r *volgaRelay) Send(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > r.cfg.MaxPacketSize {
		return fmt.Errorf("Volga packet too large: %d > %d", len(data), r.cfg.MaxPacketSize)
	}
	copyOfData := append([]byte(nil), data...)

	select {
	case <-r.ctx.Done():
		return errors.New("Volga relay stopped")
	case r.queue <- copyOfData:
		return nil
	default:
		r.stats.queueDrops.Add(1)
		return errors.New("Volga relay queue full")
	}
}

func (r *volgaRelay) batchLoop() {
	defer r.wg.Done()

	var batch [][]byte
	var bytesInBatch int
	var timer *time.Timer
	var timerC <-chan time.Time

	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		out := make([][]byte, len(batch))
		copy(out, batch)
		select {
		case <-r.ctx.Done():
			return false
		case r.batches <- out:
		}
		batch = batch[:0]
		bytesInBatch = 0
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		timerC = nil
		return true
	}

	for {
		select {
		case <-r.ctx.Done():
			return
		case pkt := <-r.queue:
			batch = append(batch, pkt)
			bytesInBatch += len(pkt)
			if len(batch) == 1 {
				if timer == nil {
					timer = time.NewTimer(r.cfg.BatchTimeout)
				} else {
					timer.Reset(r.cfg.BatchTimeout)
				}
				timerC = timer.C
			}
			if len(batch) >= r.cfg.BatchSize || bytesInBatch >= r.cfg.BatchMaxBytes {
				if !flush() {
					return
				}
			}
		case <-timerC:
			if !flush() {
				return
			}
		}
	}
}

func (r *volgaRelay) worker(id int) {
	defer r.wg.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		case batch := <-r.batches:
			if err := r.sendBatch(batch); err != nil {
				r.stats.httpFailed.Add(1)
				r.noteSendError(err)
				utils.Debugf("[VOLGA] relay worker %d: %v", id, err)
			} else {
				r.stats.httpSent.Add(1)
				r.noteSendSuccess()
			}
		}
	}
}

func (r *volgaRelay) noteSendError(err error) {
	if isVolgaRelayAuthError(err) {
		if r.onAuthFailure != nil {
			r.authFailureOnce.Do(r.onAuthFailure)
		}
		return
	}

	threshold := r.cfg.RelayFailureThreshold
	if threshold <= 0 {
		threshold = 3
	}

	r.healthMu.Lock()
	r.consecutiveFailures++
	trigger := r.consecutiveFailures >= threshold
	r.healthMu.Unlock()

	if trigger && r.onTransportFailure != nil {
		r.transportFailureOnce.Do(func() {
			r.onTransportFailure(err)
		})
	}
}

func (r *volgaRelay) noteSendSuccess() {
	r.healthMu.Lock()
	r.consecutiveFailures = 0
	r.healthMu.Unlock()
}

func (r *volgaRelay) sendBatch(batch [][]byte) error {
	encoded, totalBytes, err := volgaEncodeBatch(batch)
	if err != nil {
		return err
	}

	opID := fmt.Sprintf("1-%d.%d", r.auth.userID, r.seq.Add(1))
	caretID := fmt.Sprintf("1-%d.%d", r.auth.userID, r.seq.Add(1))

	bundle := []interface{}{
		map[string]interface{}{
			"id":         opID,
			"frontier":   r.getFrontier(),
			"undoable":   true,
			"actionName": "textInsert",
			"ops":        []interface{}{[]interface{}{"it", "vyd:t/00000000000008", 0, "A"}},
			"sideEffect": false,
			"localId":    r.localID.Add(1),
		},
		map[string]interface{}{
			"id":         caretID,
			"frontier":   []interface{}{opID},
			"undoable":   false,
			"actionName": "setCaret",
			"ops": []interface{}{
				[]interface{}{"us", r.auth.userID, []interface{}{
					[]interface{}{
						[]interface{}{"vyd:t/00000000000008", 0, -1},
						[]interface{}{"vyd:t/00000000000008", 0, -1},
					},
				}},
			},
			"sideEffect": true,
			"localId":    r.localID.Add(1),
		},
		encoded,
	}

	payload := map[string]interface{}{
		"message": map[string]interface{}{
			"bundleId": r.bundleID.Add(1),
			"bundle":   bundle,
		},
		"targetUserId": nil,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal relay batch: %w", err)
	}

	relayURL := "https://volga.yandex.ru/session/main/" + r.auth.requestPath + "/relay"
	req, err := http.NewRequestWithContext(r.ctx, http.MethodPost, relayURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", volgaUserAgent)
	req.Header.Set("Authorization", "Bearer "+r.auth.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://volga.yandex.ru")
	req.Header.Set("Referer", "https://volga.yandex.ru/document/?request-path="+url.QueryEscape(r.auth.requestPath))
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if r.auth.relayCookieHeader != "" {
		req.Header.Set("Cookie", r.auth.relayCookieHeader)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("relay POST: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return &volgaRelayHTTPError{StatusCode: resp.StatusCode}
	}

	r.stats.batchesSent.Add(1)
	r.stats.packetsBatched.Add(uint64(len(batch)))
	r.stats.packetsSent.Add(uint64(len(batch)))
	r.stats.bytesSent.Add(uint64(totalBytes))
	return nil
}

func (r *volgaRelay) SetFrontier(id string) {
	if id == "" {
		return
	}
	r.frontierMu.Lock()
	r.frontier = id
	r.frontierMu.Unlock()
}

func (r *volgaRelay) getFrontier() []interface{} {
	r.frontierMu.RLock()
	defer r.frontierMu.RUnlock()
	if r.frontier == "" {
		return []interface{}{}
	}
	return []interface{}{r.frontier}
}

func volgaEncodeBatch(batch [][]byte) (string, int, error) {
	var buf bytes.Buffer
	buf.Grow(4 + len(batch)*2 + 4096)
	buf.WriteString(volgaWireMagic)

	var total int
	var lenBuf [2]byte
	for _, packet := range batch {
		if len(packet) == 0 || len(packet) > 0xffff {
			return "", 0, fmt.Errorf("invalid packet size %d", len(packet))
		}
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(packet)))
		buf.Write(lenBuf[:])
		buf.Write(packet)
		total += len(packet)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), total, nil
}

func volgaDecodeBatch(encoded string) ([][]byte, bool) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) < len(volgaWireMagic) || string(decoded[:len(volgaWireMagic)]) != volgaWireMagic {
		return nil, false
	}
	decoded = decoded[len(volgaWireMagic):]

	packets := make([][]byte, 0, 4)
	for len(decoded) > 0 {
		if len(decoded) < 2 {
			return nil, false
		}
		sz := int(binary.BigEndian.Uint16(decoded[:2]))
		decoded = decoded[2:]
		if sz <= 0 || len(decoded) < sz {
			return nil, false
		}
		packets = append(packets, append([]byte(nil), decoded[:sz]...))
		decoded = decoded[sz:]
	}
	return packets, len(packets) > 0
}

type volgaWS struct {
	auth  *volgaAuth
	cfg   VolgaConfig
	stats *volgaStats
	relay *volgaRelay

	onData  func([]byte)
	onState func(bool)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	connMu sync.Mutex
	conn   *websocket.Conn

	readyOnce sync.Once
	ready     chan struct{}
}

func newVolgaWS(auth *volgaAuth, cfg VolgaConfig, stats *volgaStats, relay *volgaRelay, onData func([]byte), onState func(bool)) *volgaWS {
	ctx, cancel := context.WithCancel(context.Background())
	return &volgaWS{
		auth:    auth,
		cfg:     cfg,
		stats:   stats,
		relay:   relay,
		onData:  onData,
		onState: onState,
		ctx:     ctx,
		cancel:  cancel,
		ready:   make(chan struct{}),
	}
}

func (w *volgaWS) Start() {
	w.wg.Add(1)
	go w.run()
}

func (w *volgaWS) Stop() {
	w.cancel()
	w.closeActiveConn()
	w.wg.Wait()
}

func (w *volgaWS) setActiveConn(conn *websocket.Conn) bool {
	if conn == nil {
		return false
	}
	w.connMu.Lock()
	defer w.connMu.Unlock()
	if w.ctx.Err() != nil {
		return false
	}
	w.conn = conn
	return true
}

func (w *volgaWS) clearActiveConn(conn *websocket.Conn) {
	w.connMu.Lock()
	if w.conn == conn {
		w.conn = nil
	}
	w.connMu.Unlock()
}

func (w *volgaWS) closeActiveConn() {
	w.connMu.Lock()
	conn := w.conn
	w.conn = nil
	w.connMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (w *volgaWS) WaitReady(timeout time.Duration) error {
	select {
	case <-w.ready:
		return nil
	case <-w.ctx.Done():
		return errors.New("Volga websocket stopped before becoming ready")
	case <-time.After(timeout):
		return errors.New("Volga websocket readiness timeout")
	}
}

func (w *volgaWS) run() {
	defer w.wg.Done()
	delay := w.cfg.ReconnectMinDelay

	for {
		if w.ctx.Err() != nil {
			return
		}
		connectedAt := time.Now()
		err := w.connect()
		if w.ctx.Err() != nil {
			return
		}
		if err != nil {
			utils.Debugf("[VOLGA] Xiva websocket: %v", err)
		}
		w.stats.wsReconnects.Add(1)

		if time.Since(connectedAt) > 30*time.Second {
			delay = w.cfg.ReconnectMinDelay
		}
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = time.Duration(float64(delay) * w.cfg.ReconnectMultiplier)
		if delay > w.cfg.ReconnectMaxDelay {
			delay = w.cfg.ReconnectMaxDelay
		}
	}
}

func (w *volgaWS) connect() error {
	q := url.Values{}
	q.Set("service", "volga")
	q.Set("user", w.auth.userIDStr)
	q.Set("sign", w.auth.sign)
	q.Set("ts", w.auth.ts)
	q.Set("client", "web")
	q.Set("session", w.auth.sessionID)
	q.Set("fetch_history", w.auth.userIDStr+":volga:0:1")
	q.Set("x_request_attempt", "0")
	wsURL := "wss://push.yandex.ru/v2/subscribe/websocket?" + q.Encode()

	header := http.Header{}
	header.Set("User-Agent", volgaUserAgent)
	header.Set("Origin", "https://volga.yandex.ru")
	if cookies := volgaCookieHeader(w.auth.client.Jar, wsURL); cookies != "" {
		header.Set("Cookie", cookies)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: w.cfg.WSHandshakeTimeout,
		NetDialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ReadBufferSize:  1 << 20,
		WriteBufferSize: 1 << 20,
	}
	conn, resp, err := dialer.DialContext(w.ctx, wsURL, header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial HTTP %d: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("dial: %w", err)
	}
	if !w.setActiveConn(conn) {
		_ = conn.Close()
		return errors.New("Volga websocket stopped")
	}
	defer func() {
		w.clearActiveConn(conn)
		_ = conn.Close()
	}()

	utils.Debugf("[VOLGA] Xiva websocket connected user=%s", w.auth.userIDStr)
	if w.onState != nil {
		w.onState(true)
	}
	defer func() {
		if w.onState != nil {
			w.onState(false)
		}
	}()
	w.readyOnce.Do(func() { close(w.ready) })

	for {
		if w.ctx.Err() != nil {
			return nil
		}
		_ = conn.SetReadDeadline(time.Now().Add(w.cfg.WSReadTimeout))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		w.handleMessage(msg)
	}
}

func (w *volgaWS) handleMessage(raw []byte) {
	var envelope struct {
		Operation string `json:"operation"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return
	}
	if envelope.Operation == "ping" || envelope.Message == "" {
		return
	}
	if envelope.Operation != "SESSION" && envelope.Operation != "WORKER" {
		return
	}

	var inner struct {
		T       string          `json:"t"`
		UserID  int64           `json:"userId"`
		Bundle  json.RawMessage `json:"bundle"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal([]byte(envelope.Message), &inner); err != nil {
		return
	}
	if inner.UserID == w.auth.userID {
		return
	}

	switch inner.T {
	case "relay":
		var relay struct {
			Bundle []json.RawMessage `json:"bundle"`
		}
		if err := json.Unmarshal(inner.Message, &relay); err != nil {
			return
		}
		for _, item := range relay.Bundle {
			w.handleBundleItem(item)
		}
	case "exchange":
		w.handleBundle(inner.Bundle)
	}
}

func (w *volgaWS) handleBundle(raw json.RawMessage) {
	var array []json.RawMessage
	if err := json.Unmarshal(raw, &array); err == nil {
		for _, item := range array {
			w.handleBundleItem(item)
		}
		return
	}

	var object struct {
		Value []json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		for _, item := range object.Value {
			w.handleBundleItem(item)
		}
	}
}

func (w *volgaWS) handleBundleItem(raw json.RawMessage) {
	var operation struct {
		ID         string `json:"id"`
		ActionName string `json:"actionName"`
	}
	if err := json.Unmarshal(raw, &operation); err == nil && operation.ActionName != "" {
		w.relay.SetFrontier(operation.ID)
		return
	}

	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil || encoded == "" {
		return
	}
	packets, ok := volgaDecodeBatch(encoded)
	if !ok {
		return
	}

	for _, packet := range packets {
		w.stats.packetsRecv.Add(1)
		w.stats.bytesRecv.Add(uint64(len(packet)))
		if w.onData != nil {
			w.onData(packet)
		}
	}
}

// YandexVolgaTransport implements transport.Transport on top of the current
// Yandex Volga document collaboration backend.
type YandexVolgaTransport struct {
	*transport.BaseTransport

	docURL string
	cfg    VolgaConfig
	stats  *volgaStats

	mu    sync.Mutex
	auth  *volgaAuth
	relay *volgaRelay
	ws    *volgaWS
}

func NewYandexVolgaTransport(docURL string, cfg transport.TransportConfig) *YandexVolgaTransport {
	return &YandexVolgaTransport{
		BaseTransport: transport.NewBaseTransport(cfg),
		docURL:        strings.TrimSpace(docURL),
		cfg:           DefaultVolgaConfig(),
		stats:         &volgaStats{},
	}
}

func (t *YandexVolgaTransport) Start() error {
	if t.docURL == "" {
		return errors.New("Volga document URL is empty")
	}
	if err := t.BaseTransport.Start(); err != nil {
		return err
	}

	utils.Debugf("[VOLGA] authorizing...")
	auth, err := volgaAuthorize(t.docURL)
	if err != nil {
		_ = t.BaseTransport.Stop()
		return fmt.Errorf("Volga auth: %w", err)
	}

	relay := newVolgaRelay(
		auth,
		t.cfg,
		t.stats,
		func() {
			log.Printf("[VOLGA] relay authorization rejected; full re-auth required")
			t.SetConnected(false)
		},
		func(err error) {
			log.Printf("[VOLGA] relay unhealthy after repeated send failures: %v; full re-auth required", err)
			t.SetConnected(false)
		},
	)
	relay.Start()

	ws := newVolgaWS(auth, t.cfg, t.stats, relay, func(data []byte) {
		t.RecordReceive(len(data))
		t.CallReceive(data)
	}, func(connected bool) {
		t.SetConnected(connected)
	})
	ws.Start()

	if err := ws.WaitReady(t.cfg.StartTimeout); err != nil {
		ws.Stop()
		relay.Stop()
		_ = t.BaseTransport.Stop()
		return err
	}

	t.mu.Lock()
	t.auth = auth
	t.relay = relay
	t.ws = ws
	t.mu.Unlock()

	utils.Debugf("[VOLGA] transport ready user=%d request-path=%s", auth.userID, auth.requestPath)
	return nil
}

func (t *YandexVolgaTransport) Stop() error {
	t.SetConnected(false)

	t.mu.Lock()
	ws := t.ws
	relay := t.relay
	t.ws = nil
	t.relay = nil
	t.auth = nil
	t.mu.Unlock()

	if ws != nil {
		ws.Stop()
	}
	if relay != nil {
		relay.Stop()
	}
	return t.BaseTransport.Stop()
}

func (t *YandexVolgaTransport) Send(data []byte) error {
	if !t.IsConnected() {
		return errors.New("Volga transport not connected")
	}
	t.mu.Lock()
	relay := t.relay
	t.mu.Unlock()
	if relay == nil {
		return errors.New("Volga relay is not running")
	}
	if err := relay.Send(data); err != nil {
		return err
	}
	t.RecordSend(len(data))
	return nil
}

func (t *YandexVolgaTransport) Stats() transport.TransportStats {
	base := t.BaseTransport.Stats()
	base.Reconnects = t.stats.wsReconnects.Load()
	return base
}
