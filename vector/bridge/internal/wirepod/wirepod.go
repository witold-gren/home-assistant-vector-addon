// Package wirepod reads what the bridge needs from a wire-pod server: robot addresses and SDK GUIDs.
package wirepod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Robot as listed by GET /api-sdk/get_sdk_info.
type Robot struct {
	ESN       string `json:"esn"`
	IP        string `json:"ip_address"`
	GUID      string `json:"guid"`
	Activated bool   `json:"activated"`
}

type sdkInfo struct {
	GlobalGUID string  `json:"global_guid"`
	Robots     []Robot `json:"robots"`
}

type Client struct {
	Base string
	HTTP *http.Client
}

func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Robot returns the robot `esn` (first one when empty). Its GUID falls back to the global GUID.
func (c *Client) Robot(ctx context.Context, esn string) (Robot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/api-sdk/get_sdk_info", nil)
	if err != nil {
		return Robot{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Robot{}, fmt.Errorf("wire-pod: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Robot{}, fmt.Errorf("wire-pod get_sdk_info: HTTP %d", resp.StatusCode)
	}
	var info sdkInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return Robot{}, fmt.Errorf("wire-pod get_sdk_info: %w", err)
	}
	for _, r := range info.Robots {
		if esn != "" && !strings.EqualFold(r.ESN, esn) {
			continue
		}
		if r.GUID == "" {
			r.GUID = info.GlobalGUID
		}
		return r, nil
	}
	if esn == "" {
		return Robot{}, fmt.Errorf("wire-pod knows no robot")
	}
	return Robot{}, fmt.Errorf("wire-pod knows no robot %q", esn)
}

// SayText makes the robot speak through wire-pod (POST /api-sdk/say_text), so wire-pod's voice is
// used — its Wyoming TTS (Piper) when enabled, else Vector's own. Returns after the robot spoke.
func (c *Client) SayText(ctx context.Context, esn, text string) error {
	form := url.Values{"serial": {esn}, "text": {text}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/api-sdk/say_text", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// speaking takes as long as the sentence: no client timeout, ctx bounds it
	resp, err := (&http.Client{Transport: c.HTTP.Transport}).Do(req)
	if err != nil {
		return fmt.Errorf("wire-pod: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	// wire-pod answers 200 with "error: …" on failure
	if resp.StatusCode != http.StatusOK || strings.HasPrefix(msg, "error") {
		return fmt.Errorf("wire-pod say_text: %s", msg)
	}
	return nil
}

// CustomIntent as wire-pod stores it (/api/get_custom_intents_json).
type CustomIntent struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Utterances  []string `json:"utterances"`
	Intent      string   `json:"intent"`
	Params      struct {
		ParamName  string `json:"paramname"`
		ParamValue string `json:"paramvalue"`
	} `json:"params"`
	Exec      string   `json:"exec"`
	ExecArgs  []string `json:"execargs"`
	IsSystem  bool     `json:"issystem"`
	LuaScript string   `json:"luascript"`
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wire-pod: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("wire-pod %s: HTTP %d %s", path, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// CustomIntents lists the custom intents in wire-pod's order (the order it matches them in).
func (c *Client) CustomIntents(ctx context.Context) ([]CustomIntent, error) {
	out, err := c.do(ctx, http.MethodGet, "/api/get_custom_intents_json", nil)
	if err != nil {
		// wire-pod answers 400 "you must create an intent first" when there are none
		if strings.Contains(string(out), "create an intent first") {
			return nil, nil
		}
		return nil, err
	}
	var list []CustomIntent
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("wire-pod custom intents: %w", err)
	}
	return list, nil
}

// AddCustomIntent appends an intent (wire-pod keeps `issystem` only when adding, not when editing).
func (c *Client) AddCustomIntent(ctx context.Context, in CustomIntent) error {
	_, err := c.do(ctx, http.MethodPost, "/api/add_custom_intent", in)
	return err
}

// RemoveCustomIntent removes the intent at 1-based position `number`.
func (c *Client) RemoveCustomIntent(ctx context.Context, number int) error {
	_, err := c.do(ctx, http.MethodPost, "/api/remove_custom_intent", map[string]int{"number": number})
	return err
}

// SDKCall calls one of wire-pod's simple SDK endpoints for a robot (e.g. /api-sdk/time_format_24).
func (c *Client) SDKCall(ctx context.Context, path, esn string) error {
	out, err := c.do(ctx, http.MethodGet, path+"?serial="+url.QueryEscape(esn), nil)
	if err != nil {
		return err
	}
	if msg := strings.TrimSpace(string(out)); strings.HasPrefix(msg, "error") {
		return fmt.Errorf("wire-pod %s: %s", path, msg)
	}
	return nil
}

// KGAPI returns wire-pod's LLM ("knowledge graph") settings.
func (c *Client) KGAPI(ctx context.Context) (map[string]any, error) {
	out, err := c.do(ctx, http.MethodGet, "/api/get_kg_api", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("wire-pod get_kg_api: %w", err)
	}
	return m, nil
}

// SetKGAPI changes the given LLM settings (wire-pod merges them into its config).
func (c *Client) SetKGAPI(ctx context.Context, fields map[string]any) error {
	_, err := c.do(ctx, http.MethodPost, "/api/set_kg_api", fields)
	return err
}
