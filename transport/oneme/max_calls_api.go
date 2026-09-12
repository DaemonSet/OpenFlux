package oneme

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const maxCallsAPIEndpoint = "https://calls.okcdn.ru/fb.do"

var maxCallsHTTPClient = &http.Client{Timeout: 30 * time.Second}

type maxCallsLogin struct {
	SessionKey     string `json:"session_key"`
	ExternalUserID string `json:"external_user_id"`
}

type maxStartedConversation struct {
	Endpoint string `json:"endpoint"`
}

func (c *MaxClient) initializeCallsAPI() error {
	resp, err := c.invoke(158, map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("request call token: %w", err)
	}

	var tokenPayload struct {
		Token string `json:"token"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(resp.Payload, &tokenPayload); err != nil {
		return fmt.Errorf("decode call-token response: %w", err)
	}
	if tokenPayload.Error != "" {
		return fmt.Errorf("request call token: %s", tokenPayload.Error)
	}
	if tokenPayload.Token == "" {
		return fmt.Errorf("call-token response contains no token")
	}

	sessionData, err := json.Marshal(map[string]interface{}{
		"auth_token":     tokenPayload.Token,
		"client_type":    "SDK_JS",
		"client_version": "1.1",
		"device_id":      c.deviceID,
		"version":        3,
	})
	if err != nil {
		return fmt.Errorf("encode Calls API session: %w", err)
	}

	body, err := callMaxCallsAPI(url.Values{
		"method":       {"auth.anonymLogin"},
		"session_data": {string(sessionData)},
	})
	if err != nil {
		return fmt.Errorf("login to Calls API: %w", err)
	}
	if err := decodeCallsAPIResponse(body, &c.callsLogin); err != nil {
		return fmt.Errorf("login to Calls API: %w", err)
	}
	if c.callsLogin.SessionKey == "" || c.callsLogin.ExternalUserID == "" {
		return fmt.Errorf("Calls API login response is missing session identity")
	}

	fmt.Printf("[MAX] Calls external user ID: %s\n", c.callsLogin.ExternalUserID)
	return nil
}

func (c *MaxClient) startConversation(calleeID int64) (string, error) {
	body, err := callMaxCallsAPI(url.Values{
		"method":          {"vchat.startConversation"},
		"session_key":     {c.callsLogin.SessionKey},
		"conversationId":  {genUUID()},
		"isVideo":         {"false"},
		"protocolVersion": {"5"},
		"externalIds":     {strconv.FormatInt(calleeID, 10)},
		"payload":         {`{"is_video":false}`},
	})
	if err != nil {
		return "", err
	}

	var started maxStartedConversation
	if err := decodeCallsAPIResponse(body, &started); err != nil {
		return "", err
	}
	if started.Endpoint == "" {
		return "", fmt.Errorf("Calls API response contains no signaling endpoint")
	}

	u, err := url.Parse(started.Endpoint)
	if err != nil {
		return "", fmt.Errorf("parse signaling endpoint: %w", err)
	}

	q := u.Query()
	q.Set("platform", "WEB")
	q.Set("appVersion", "1.1")
	q.Set("version", "5")
	q.Set("device", "browser")
	q.Set("capabilities", "2A03F")
	q.Set("clientType", "ONE_ME")
	q.Set("tgt", "start")
	u.RawQuery = q.Encode()

	return u.String(), nil
}

func callMaxCallsAPI(values url.Values) ([]byte, error) {
	values.Set("format", "JSON")
	values.Set("application_key", "CNHIJPLGDIHBABABA")

	resp, err := maxCallsHTTPClient.PostForm(maxCallsAPIEndpoint, values)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP status %s", resp.Status)
	}
	return body, nil
}

func decodeCallsAPIResponse(body []byte, target interface{}) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("invalid JSON response")
	}
	if raw, ok := envelope["error_code"]; ok {
		var code interface{}
		_ = json.Unmarshal(raw, &code)
		var message string
		_ = json.Unmarshal(envelope["error_msg"], &message)
		if message == "" {
			message = "unspecified error"
		}
		return fmt.Errorf("MAX error %v: %s", code, message)
	}
	if raw, ok := envelope["error"]; ok {
		var message string
		if json.Unmarshal(raw, &message) == nil && message != "" {
			return fmt.Errorf("MAX error: %s", message)
		}
		return fmt.Errorf("MAX returned an error")
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
