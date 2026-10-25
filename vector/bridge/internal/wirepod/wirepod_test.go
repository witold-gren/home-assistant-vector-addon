package wirepod

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestRobot(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api-sdk/get_sdk_info" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"global_guid":"global","robots":[
			{"esn":"00aa","ip_address":"10.0.0.2","guid":"","activated":true},
			{"esn":"00a1b2c3","ip_address":"192.168.1.50","guid":"own","activated":true}]}`))
	}))
	defer hs.Close()
	c := New(hs.URL + "/")
	ctx := context.Background()

	r, err := c.Robot(ctx, "")
	if err != nil || r.ESN != "00aa" || r.GUID != "global" {
		t.Fatalf("first robot falls back to global guid: %+v %v", r, err)
	}
	r, err = c.Robot(ctx, "00A1B2C3")
	if err != nil || r.IP != "192.168.1.50" || r.GUID != "own" {
		t.Fatalf("by esn: %+v %v", r, err)
	}
	if _, err := c.Robot(ctx, "nope"); err == nil {
		t.Fatal("unknown esn must fail")
	}
}

func TestSayText(t *testing.T) {
	var got url.Values
	reply := "success"
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		_, _ = w.Write([]byte(reply))
	}))
	defer hs.Close()
	c := New(hs.URL)
	if err := c.SayText(context.Background(), "00a1b2c3", "Hello Zoë"); err != nil {
		t.Fatal(err)
	}
	if got.Get("serial") != "00a1b2c3" || got.Get("text") != "Hello Zoë" {
		t.Fatalf("form: %v", got)
	}
	reply = "error: robot not found"
	if err := c.SayText(context.Background(), "x", "y"); err == nil {
		t.Fatal("wire-pod error text must fail")
	}
}
