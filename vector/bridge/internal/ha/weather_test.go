package ha

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeHA serves /api/states, /api/states/<id> and weather.get_forecasts on localhost.
func fakeHA(t *testing.T, states map[string]map[string]any, seen *[]string) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "401", http.StatusUnauthorized)
			return
		}
		if seen != nil {
			*seen = append(*seen, r.Method+" "+r.URL.RequestURI())
		}
		switch {
		case r.URL.Path == "/api/states":
			var all []map[string]any
			for id, s := range states {
				all = append(all, map[string]any{"entity_id": id, "state": s["state"], "attributes": s})
			}
			all = append(all, map[string]any{"entity_id": "light.x", "state": "on", "attributes": map[string]any{}})
			_ = json.NewEncoder(w).Encode(all)
		case strings.HasPrefix(r.URL.Path, "/api/states/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/states/")
			s, ok := states[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"entity_id": id, "state": s["state"], "attributes": s})
		case r.URL.Path == "/api/services/weather/get_forecasts" && r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(b, &req)
			if req["type"] != "daily" {
				http.Error(w, "bad type", 400)
				return
			}
			id := req["entity_id"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{"changed_states": []any{}, "service_response": map[string]any{
				id: map[string]any{"forecast": []any{
					map[string]any{"datetime": "2026-11-24T11:00:00+00:00", "condition": "rainy", "temperature": 7.5, "templow": 2.0, "precipitation": 3.1},
					map[string]any{"datetime": "2026-11-25T11:00:00+00:00", "condition": "snowy", "temperature": 1.0},
					map[string]any{"datetime": "2026-11-26T11:00:00+00:00", "condition": "sunny", "temperature": 4.0},
				}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(hs.Close)
	return hs
}

var home = map[string]map[string]any{
	"weather.home":   {"state": "sunny", "temperature": 21.4, "temperature_unit": "°C", "humidity": 40.0, "wind_speed": 12.0, "wind_speed_unit": "km/h"},
	"weather.garden": {"state": "unavailable"},
}

func TestWeatherNow(t *testing.T) {
	hs := fakeHA(t, home, nil)
	c := New(hs.URL, "tok", "weather.home")
	w, err := c.Weather(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Entity != "weather.home" || w.ConditionPL != "słonecznie" || *w.Temperature != 21.4 || w.Unit != "°C" || w.WindUnit != "km/h" {
		t.Fatalf("%+v", w)
	}
	if w.Text != "Na dworze jest słonecznie, 21 stopni." || w.Forecast != nil {
		t.Fatalf("text %q forecast %v", w.Text, w.Forecast)
	}
}

func TestWeatherFirstEntityAndForecast(t *testing.T) {
	var seen []string
	hs := fakeHA(t, map[string]map[string]any{"weather.b": {"state": "cloudy", "temperature": 2.0},
		"weather.a": {"state": "fog", "temperature": -1.0, "supported_features": 3.0}}, &seen)
	c := New(hs.URL, "tok", "")
	w, err := c.Weather(context.Background(), "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if w.Entity != "weather.a" || w.Text != "Na dworze jest mgła, -1 stopień." {
		t.Fatalf("%+v", w)
	}
	if len(w.Forecast) != 2 || w.Forecast[0].ConditionPL != "deszcz" || *w.Forecast[0].TempLow != 2 || w.Forecast[1].Condition != "snowy" {
		t.Fatalf("forecast %+v", w.Forecast)
	}
	if !strings.Contains(strings.Join(seen, "|"), "POST /api/services/weather/get_forecasts?return_response") {
		t.Fatalf("calls %v", seen)
	}
}

// An entity without the daily forecast (supported_features 0 or only hourly): the weather now,
// no error, no get_forecasts call (Home Assistant would answer HTTP 400).
func TestWeatherWithoutDailyForecast(t *testing.T) {
	for _, features := range []any{nil, 0.0, 2.0} {
		var seen []string
		st := map[string]any{"state": "snowy-rainy", "temperature": 2.0}
		if features != nil {
			st["supported_features"] = features
		}
		hs := fakeHA(t, map[string]map[string]any{"weather.now": st}, &seen)
		w, err := New(hs.URL, "tok", "weather.now").Weather(context.Background(), "", 1)
		if err != nil {
			t.Fatalf("features %v: %v", features, err)
		}
		if w.Text != "Na dworze jest deszcz ze śniegiem, 2 stopnie." || w.Forecast != nil || !w.NoForecast {
			t.Fatalf("features %v: %+v", features, w)
		}
		if strings.Contains(strings.Join(seen, "|"), "get_forecasts") {
			t.Fatalf("features %v: forecast asked: %v", features, seen)
		}
		b, _ := json.Marshal(w)
		if !strings.Contains(string(b), `"forecast_unavailable":true`) {
			t.Fatalf("json %s", b)
		}
	}
	// Without a forecast asked for, the flag stays off.
	hs := fakeHA(t, map[string]map[string]any{"weather.now": {"state": "sunny"}}, nil)
	if w, err := New(hs.URL, "tok", "weather.now").Weather(context.Background(), "", 0); err != nil || w.NoForecast {
		t.Fatalf("%+v %v", w, err)
	}
}

func TestWeatherErrors(t *testing.T) {
	var nilClient *Client
	if _, err := nilClient.Weather(context.Background(), "", 0); err != ErrNoHA {
		t.Fatalf("nil client: %v", err)
	}
	if New("", "tok", "") != nil || New("http://x", "", "") != nil {
		t.Fatal("client without url/token")
	}
	hs := fakeHA(t, home, nil)
	c := New(hs.URL, "tok", "")
	for entity, want := range map[string]string{
		"weather.garden":  "unavailable",
		"weather.nowhere": "no entity",
		"light.x":         "not a weather entity",
		"weather.x/../a":  "not a weather entity",
	} {
		if _, err := c.Weather(context.Background(), entity, 0); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", entity, err)
		}
	}
	bad := New(hs.URL, "wrong", "weather.home")
	if _, err := bad.Weather(context.Background(), "", 0); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("bad token: %v", err)
	}
	empty := New(fakeHA(t, map[string]map[string]any{}, nil).URL, "tok", "")
	if _, err := empty.Weather(context.Background(), "", 0); err == nil || !strings.Contains(err.Error(), "no weather entity") {
		t.Fatalf("no entity: %v", err)
	}
}

func TestDegrees(t *testing.T) {
	for n, want := range map[int]string{0: "stopni", 1: "stopień", -1: "stopień", 2: "stopnie", 4: "stopnie", 5: "stopni",
		12: "stopni", 14: "stopni", 22: "stopnie", 25: "stopni", -3: "stopnie", 112: "stopni"} {
		if got := degrees(n); got != want {
			t.Errorf("%d: %s, want %s", n, got, want)
		}
	}
}
