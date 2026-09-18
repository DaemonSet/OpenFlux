// Package mailru implements a transport that tunnels OpenFlux frames through
// Mail.ru's public cloud document editor collaboration channel.
package mailru

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"universal-bypass-tool/transport"
	"universal-bypass-tool/utils"
)

const mailruUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36"

var cursorPayloadRE = regexp.MustCompile(`"cursor":"[^;]+;([^"]+)"`)

type docsInfo struct {
	token        string
	docKey       string
	wsURL        string
	fileType     string
	docURL       string
	docTitle     string
	permissions  map[string]interface{}
	callbackURL  string
	editorUserID string
}

type docSession struct {
	info    docsInfo
	conn    *websocket.Conn
	userID  string
	writeMu sync.Mutex
}

func (s *docSession) safeWrite(messageType int, data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.conn == nil {
		return fmt.Errorf("Mail.ru websocket is not connected")
	}
	return s.conn.WriteMessage(messageType, data)
}

type DocsTransport struct {
	*transport.BaseTransport

	weblink string
	session *docSession

	userCounter atomic.Int32
	baseUserID  string
}

// NormalizeWeblink accepts either the short public weblink
// ("AbCdEfGh1/IjKlMnOp2") or a full cloud.mail.ru public URL and returns the
// canonical short form. Both peers should derive encryption context from this
// canonical value so equivalent URL spellings remain interoperable.
func NormalizeWeblink(weblink string) string {
	weblink = strings.TrimSpace(weblink)
	for _, prefix := range []string{
		"https://cloud.mail.ru/public/",
		"http://cloud.mail.ru/public/",
		"https://cloud.mail.ru/",
		"http://cloud.mail.ru/",
	} {
		if strings.HasPrefix(weblink, prefix) {
			return strings.Trim(strings.TrimPrefix(weblink, prefix), "/")
		}
	}
	return strings.Trim(weblink, "/")
}

func NewDocsTransport(weblink string, config transport.TransportConfig) *DocsTransport {
	t := &DocsTransport{
		BaseTransport: transport.NewBaseTransport(config),
		weblink:       NormalizeWeblink(weblink),
	}
	t.baseUserID = randomUserID()
	return t
}

func (t *DocsTransport) Start() error {
	if t.weblink == "" {
		return fmt.Errorf("Mail.ru public document reference is empty")
	}
	if err := t.BaseTransport.Start(); err != nil {
		return err
	}
	t.baseUserID = randomUserID()
	utils.SafeGo("mailru.keepAlive", t.keepAliveLoop)
	t.connectToDoc(0)
	return nil
}

func (t *DocsTransport) Stop() error {
	if err := t.BaseTransport.Stop(); err != nil {
		return err
	}
	t.Mu.Lock()
	session := t.session
	t.session = nil
	t.Mu.Unlock()
	if session != nil && session.conn != nil {
		_ = session.conn.Close()
	}
	return nil
}

func (t *DocsTransport) Send(data []byte) error {
	if !t.IsConnected() {
		return fmt.Errorf("Mail.ru transport not connected")
	}

	t.Mu.RLock()
	session := t.session
	t.Mu.RUnlock()
	if session == nil {
		return fmt.Errorf("Mail.ru transport has no active session")
	}

	payload := base64.StdEncoding.EncodeToString(data)
	message := fmt.Sprintf(`42["message",{"type":"cursor","cursor":"18;%s"}]`, payload)
	if err := session.safeWrite(websocket.TextMessage, []byte(message)); err != nil {
		t.SetConnected(false)
		return err
	}
	t.RecordSend(len(data))
	return nil
}

func (t *DocsTransport) connectToDoc(attempt int) {
	if !t.IsRunning() {
		return
	}
	utils.Debugf("[M-DOCS] connect attempt %d", attempt)

	utils.SafeGo("mailru.connect", func() {
		if !t.IsRunning() {
			return
		}

		suffix := fmt.Sprintf("%03d", t.userCounter.Add(1)%1000)
		userID := t.baseUserID + suffix

		info, err := t.fetchDocInfo(t.weblink)
		if err != nil {
			utils.Debugf("[M-DOCS] fetchDocInfo failed: %v", err)
			t.scheduleReconnect(attempt)
			return
		}

		dialer := websocket.Dialer{
			HandshakeTimeout: 15 * time.Second,
			NetDialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		}
		headers := http.Header{}
		headers.Set("User-Agent", mailruUserAgent)
		headers.Set("Origin", "https://docs.datacloudmail.ru")

		conn, resp, err := dialer.Dial(info.wsURL, headers)
		if err != nil {
			status := 0
			if resp != nil {
				status = resp.StatusCode
			}
			utils.Debugf("[M-DOCS] websocket dial failed (http %d): %v", status, err)
			t.scheduleReconnect(attempt)
			return
		}
		if !t.IsRunning() {
			_ = conn.Close()
			return
		}

		session := &docSession{info: info, conn: conn, userID: userID}
		t.Mu.Lock()
		old := t.session
		t.session = session
		t.SetConnected(true)
		t.Mu.Unlock()
		if old != nil && old.conn != nil && old.conn != conn {
			_ = old.conn.Close()
		}

		if err := t.sendAuth(session); err != nil {
			utils.Debugf("[M-DOCS] auth write failed: %v", err)
			t.SetConnected(false)
			_ = conn.Close()
			t.scheduleReconnect(attempt)
			return
		}

		connectedAt := time.Now()
		for t.IsRunning() {
			_, message, err := conn.ReadMessage()
			if err != nil {
				if !t.IsRunning() {
					return
				}
				utils.Debugf("[M-DOCS] read failed: %v", err)
				t.SetConnected(false)
				_ = conn.Close()
				next := attempt
				if time.Since(connectedAt) > 15*time.Second {
					next = -1
				}
				t.scheduleReconnect(next)
				return
			}
			t.handleMessage(session, message)
		}
	})
}

func (t *DocsTransport) sendAuth(session *docSession) error {
	if err := session.safeWrite(websocket.TextMessage, []byte(fmt.Sprintf(`40{"token":"%s"}`, session.info.token))); err != nil {
		return err
	}

	authMessage := map[string]interface{}{
		"type":                "auth",
		"docid":               session.info.docKey,
		"documentCallbackUrl": session.info.callbackURL,
		"token":               "fghhfgsjdgfjs",
		"user": map[string]interface{}{
			"id":        session.info.editorUserID,
			"username":  session.userID,
			"indexUser": -1,
		},
		"editorType":         0,
		"lastOtherSaveTime":  -1,
		"block":              []interface{}{},
		"documentFormatSave": 65,
		"view":               false,
		"isCloseCoAuthoring": false,
		"openCmd": map[string]interface{}{
			"c":               "open",
			"id":              session.info.docKey,
			"userid":          session.info.editorUserID,
			"format":          session.info.fileType,
			"url":             session.info.docURL,
			"title":           session.info.docTitle,
			"lcid":            25,
			"nobase64":        true,
			"convertToOrigin": ".pdf.xps.oxps.djvu",
		},
		"lang":                  "ru",
		"mode":                  "edit",
		"permissions":           session.info.permissions,
		"IsAnonymousUser":       false,
		"timezoneOffset":        -180,
		"coEditingMode":         "fast",
		"jwtOpen":               session.info.token,
		"time":                  1000,
		"supportAuthChangesAck": true,
	}
	part, err := json.Marshal([]interface{}{"message", authMessage})
	if err != nil {
		return err
	}
	return session.safeWrite(websocket.TextMessage, []byte("42"+string(part)))
}

func (t *DocsTransport) keepAliveLoop() {
	interval := t.GetConfig().KeepAliveInterval
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	message := []byte(`42["message",{"type":"cursor","cursor":"18;---KA---"}]`)

	for t.IsRunning() {
		<-ticker.C
		if !t.IsRunning() {
			return
		}
		t.Mu.RLock()
		session := t.session
		t.Mu.RUnlock()
		if session == nil || session.conn == nil {
			continue
		}
		if err := session.safeWrite(websocket.TextMessage, message); err != nil {
			utils.Debugf("[M-DOCS] keep-alive failed: %v", err)
			t.SetConnected(false)
		}
	}
}

func (t *DocsTransport) handleMessage(session *docSession, data []byte) {
	text := string(data)
	if strings.Contains(text, "---KA---") {
		return
	}
	if text == "2" {
		_ = session.safeWrite(websocket.TextMessage, []byte("3"))
		return
	}
	if text == "3" {
		return
	}
	if strings.Contains(text, `"type":"auth"`) && strings.Contains(text, `"result":1`) {
		utils.Debugf("[M-DOCS] auth OK for user %s", session.userID)
		return
	}
	if !strings.Contains(text, "cursor") {
		return
	}
	match := cursorPayloadRE.FindStringSubmatch(text)
	if len(match) < 2 || match[1] == "" {
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(match[1])
	if err != nil {
		return
	}
	t.RecordReceive(len(decoded))
	t.CallReceive(decoded)
}

func (t *DocsTransport) scheduleReconnect(attempt int) {
	next := attempt + 1
	if !t.IsRunning() || next >= t.GetConfig().MaxReconnectAttempts {
		return
	}
	delay := reconnectBackoff(next)
	utils.Debugf("[M-DOCS] reconnecting in %v (attempt %d)", delay, next)
	time.Sleep(delay)
	if !t.IsRunning() {
		return
	}
	t.RecordReconnect()
	t.connectToDoc(next)
}

func reconnectBackoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	shift := n - 1
	if shift > 5 {
		shift = 5
	}
	delay := 500 * time.Millisecond * time.Duration(1<<uint(shift))
	if delay > 15*time.Second {
		delay = 15 * time.Second
	}
	delay += time.Duration(rand.Int63n(int64(delay/2) + 1))
	return delay
}

func (t *DocsTransport) fetchDocInfo(weblink string) (docsInfo, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	reqBody := map[string]string{
		"x-email":  "anonym",
		"public":   "/" + weblink,
		"platform": "desktop_web",
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return docsInfo{}, err
	}

	req, err := http.NewRequest(http.MethodPost, "https://cloud.mail.ru/api/v4/r7/edit", bytes.NewReader(jsonData))
	if err != nil {
		return docsInfo{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", mailruUserAgent)
	req.Header.Set("X-Api-Version", "4")
	req.Header.Set("Referer", fmt.Sprintf("https://cloud.mail.ru/public/%s?weblink=%s", weblink, weblink))

	resp, err := client.Do(req)
	if err != nil {
		return docsInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return docsInfo{}, fmt.Errorf("Mail.ru editor API returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return docsInfo{}, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return docsInfo{}, fmt.Errorf("parse Mail.ru editor response: %w", err)
	}

	apiBase, _ := result["api"].(string)
	token, _ := result["token"].(string)
	document, ok := result["document"].(map[string]interface{})
	if !ok || document == nil {
		return docsInfo{}, fmt.Errorf("Mail.ru editor response has no document object")
	}
	editorConfig, ok := result["editorConfig"].(map[string]interface{})
	if !ok || editorConfig == nil {
		return docsInfo{}, fmt.Errorf("Mail.ru editor response has no editorConfig object")
	}

	docKey, _ := document["key"].(string)
	fileType, _ := document["fileType"].(string)
	docURL, _ := document["url"].(string)
	docTitle, _ := document["title"].(string)
	permissions, _ := document["permissions"].(map[string]interface{})
	if permissions == nil {
		permissions = make(map[string]interface{})
	}
	callbackURL, _ := editorConfig["callbackUrl"].(string)
	user, _ := editorConfig["user"].(map[string]interface{})
	editorUserID := ""
	if user != nil {
		editorUserID, _ = user["id"].(string)
	}

	if apiBase == "" || token == "" || docKey == "" || docURL == "" {
		return docsInfo{}, fmt.Errorf("Mail.ru editor response is incomplete")
	}
	wsBase := strings.Replace(apiBase, "https://", "wss://", 1)
	if wsBase == apiBase {
		wsBase = strings.Replace(apiBase, "http://", "ws://", 1)
	}

	return docsInfo{
		token:        token,
		docKey:       docKey,
		wsURL:        fmt.Sprintf("%s/doc/%s/c/?EIO=4&transport=websocket", wsBase, docKey),
		fileType:     fileType,
		docURL:       docURL,
		docTitle:     docTitle,
		permissions:  permissions,
		callbackURL:  callbackURL,
		editorUserID: editorUserID,
	}, nil
}

func randomUserID() string {
	source := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("%010d", source.Intn(1000000000))
}
