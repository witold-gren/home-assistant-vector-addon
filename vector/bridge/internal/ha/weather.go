// Package ha reads the weather from Home Assistant (the add-on's homeassistant_api: the Supervisor
// proxy http://supervisor/core with SUPERVISOR_TOKEN). Read only: one state and the
// weather.get_forecasts service (which changes nothing).
package ha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Client of the Home Assistant REST API.
type Client struct {
	URL    string // e.g. http://supervisor/core
	Token  string
	Entity string // default weather entity ("" = the first weather.*)
	HTTP   *http.Client
}

// New: empty url or token = no Home Assistant (nil).
func New(url, token, entity string) *Client {
	url, token = strings.TrimRight(strings.TrimSpace(url), "/"), strings.TrimSpace(token)
	if url == "" || token == "" {
		return nil
	}
	return &Client{URL: url, Token: token, Entity: strings.TrimSpace(entity), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// ErrNoHA: the add-on has no Home Assistant access.
var ErrNoHA = errors.New("weather needs Home Assistant (homeassistant_api)")

var entityRe = regexp.MustCompile(`^weather\.[a-z0-9_]+$`)

// ConditionsPL: Home Assistant weather conditions in Polish, as a child hears them.
var ConditionsPL = map[string]string{
	"clear-night":     "bezchmurna noc",
	"cloudy":          "pochmurno",
	"exceptional":     "wyjątkowa pogoda",
	"fog":             "mgła",
	"hail":            "grad",
	"lightning":       "burza",
	"lightning-rainy": "burza z deszczem",
	"partlycloudy":    "częściowe zachmurzenie",
	"pouring":         "ulewa",
	"rainy":           "deszcz",
	"snowy":           "śnieg",
	"snowy-rainy":     "deszcz ze śniegiem",
	"sunny":           "słonecznie",
	"windy":           "wietrznie",
	"windy-variant":   "wietrznie i pochmurno",
}

// Day of a forecast.
type Day struct {
	Date        string   `json:"date"` // YYYY-MM-DD (local)
	Condition   string   `json:"condition"`
	ConditionPL string   `json:"condition_pl"`
	Temperature *float64 `json:"temperature"`
	TempLow     *float64 `json:"templow"`
	Rain        *float64 `json:"precipitation"`
}

// Weather now (and the forecast when asked for).
type Weather struct {
	Entity      string   `json:"entity"`
	Condition   string   `json:"condition"`
	ConditionPL string   `json:"condition_pl"`
	Temperature *float64 `json:"temperature"`
	Unit        string   `json:"unit"`
	Humidity    *float64 `json:"humidity"`
	WindSpeed   *float64 `json:"wind_speed"`
	WindUnit    string   `json:"wind_unit"`
	Text        string   `json:"text"` // a ready sentence in Polish
	Forecast    []Day    `json:"forecast,omitempty"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("home assistant: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("home assistant %s: HTTP %d", path, resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

var errNotFound = errors.New("not found")

type state struct {
	EntityID   string         `json:"entity_id"`
	State      string         `json:"state"`
	Attributes map[string]any `json:"attributes"`
}

func num(v any) *float64 {
	if f, ok := v.(float64); ok && !math.IsNaN(f) {
		return &f
	}
	return nil
}

func str(v any) string { s, _ := v.(string); return s }

// entity: the asked one, the option, or the first weather.* (sorted by id).
func (c *Client) entity(ctx context.Context, asked string) (string, error) {
	e := strings.TrimSpace(asked)
	if e == "" {
		e = c.Entity
	}
	if e != "" {
		if !entityRe.MatchString(e) {
			return "", fmt.Errorf("entity: %q is not a weather entity", e)
		}
		return e, nil
	}
	var all []state
	if err := c.do(ctx, http.MethodGet, "/api/states", nil, &all); err != nil {
		return "", err
	}
	var ids []string
	for _, s := range all {
		if strings.HasPrefix(s.EntityID, "weather.") {
			ids = append(ids, s.EntityID)
		}
	}
	if len(ids) == 0 {
		return "", errors.New("no weather entity in Home Assistant")
	}
	sort.Strings(ids)
	return ids[0], nil
}

// Weather reads the weather now; days > 0 adds a daily forecast (at most 5 days).
func (c *Client) Weather(ctx context.Context, entity string, days int) (Weather, error) {
	if c == nil {
		return Weather{}, ErrNoHA
	}
	id, err := c.entity(ctx, entity)
	if err != nil {
		return Weather{}, err
	}
	var s state
	if err := c.do(ctx, http.MethodGet, "/api/states/"+id, nil, &s); err != nil {
		if err == errNotFound {
			return Weather{}, fmt.Errorf("no entity %s", id)
		}
		return Weather{}, err
	}
	if s.State == "unavailable" || s.State == "unknown" {
		return Weather{}, fmt.Errorf("%s is %s", id, s.State)
	}
	w := Weather{
		Entity: id, Condition: s.State, ConditionPL: conditionPL(s.State),
		Temperature: num(s.Attributes["temperature"]), Unit: str(s.Attributes["temperature_unit"]),
		Humidity: num(s.Attributes["humidity"]), WindSpeed: num(s.Attributes["wind_speed"]),
		WindUnit: str(s.Attributes["wind_speed_unit"]),
	}
	if w.Unit == "" {
		w.Unit = "°C"
	}
	w.Text = sentence(w)
	if days > 0 {
		f, err := c.forecast(ctx, id, min(days, 5))
		if err != nil {
			return Weather{}, err
		}
		w.Forecast = f
	}
	return w, nil
}

func conditionPL(c string) string {
	if pl, ok := ConditionsPL[c]; ok {
		return pl
	}
	return c
}

// sentence: "Na dworze jest słonecznie, 21 stopni."
func sentence(w Weather) string {
	s := "Na dworze jest " + w.ConditionPL
	if w.Temperature != nil {
		t := int(math.Round(*w.Temperature))
		s += fmt.Sprintf(", %d %s", t, degrees(t))
		if w.Unit == "°F" {
			s += " Fahrenheita"
		}
	}
	return s + "."
}

// degrees: Polish plural of "stopień".
func degrees(n int) string {
	a := n
	if a < 0 {
		a = -a
	}
	switch {
	case a == 1:
		return "stopień"
	case a%10 >= 2 && a%10 <= 4 && (a%100 < 12 || a%100 > 14):
		return "stopnie"
	}
	return "stopni"
}

func (c *Client) forecast(ctx context.Context, id string, days int) ([]Day, error) {
	var out struct {
		ServiceResponse map[string]struct {
			Forecast []map[string]any `json:"forecast"`
		} `json:"service_response"`
	}
	err := c.do(ctx, http.MethodPost, "/api/services/weather/get_forecasts?return_response",
		map[string]any{"entity_id": id, "type": "daily"}, &out)
	if err != nil {
		return nil, fmt.Errorf("forecast: %w", err)
	}
	list := out.ServiceResponse[id].Forecast
	res := []Day{}
	for _, f := range list {
		if len(res) == days {
			break
		}
		date := str(f["datetime"])
		if t, err := time.Parse(time.RFC3339, date); err == nil {
			date = t.In(time.Local).Format("2006-01-02")
		}
		cond := str(f["condition"])
		res = append(res, Day{Date: date, Condition: cond, ConditionPL: conditionPL(cond),
			Temperature: num(f["temperature"]), TempLow: num(f["templow"]), Rain: num(f["precipitation"])})
	}
	return res, nil
}
