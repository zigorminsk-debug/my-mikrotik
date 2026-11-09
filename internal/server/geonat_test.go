package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestLoadCountry(t *testing.T) {
	geoCache.Lock()
	delete(geoCache.data, "by")
	geoCache.Unlock()
	defer func() { geoCache.Lock(); delete(geoCache.data, "by"); geoCache.Unlock() }()
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "api.github.com" || !strings.HasSuffix(req.URL.Path, "/country/by/ipv4-aggregated.txt") {
			t.Fatalf("unexpected URL %s", req.URL)
		}
		body := fmt.Sprintf(`{"encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte("# metadata\n5.44.44.0/24\n")))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	a, _, err := loadCountry(context.Background(), client, "by")
	if err != nil || len(a) != 1 || a[0] != "5.44.44.0/24" {
		t.Fatalf("result %v %v", a, err)
	}
	_, _, err = loadCountry(context.Background(), client, "by")
	if err != nil || calls != 1 {
		t.Fatalf("cache not used: %d %v", calls, err)
	}
	if _, _, err = loadCountry(context.Background(), client, "../../bad"); err == nil {
		t.Fatal("unsafe code accepted")
	}
}
func TestGeoEndpointsRejectInvalid(t *testing.T) {
	for _, body := range []string{`{"countries":[]}`, `{"countries":["../../x"]}`} {
		r := httptest.NewRequest(http.MethodPost, "/api/geo/prefixes", strings.NewReader(body))
		w := httptest.NewRecorder()
		handleGeoPrefixes(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unexpected status %d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	handleGeoCountries(w, httptest.NewRequest(http.MethodGet, "/api/geo/countries", nil))
	if !strings.Contains(w.Body.String(), `"by"`) {
		t.Fatal("missing Belarus")
	}
	geoCache.Lock()
	geoCache.data["by"] = geoCacheEntry{prefixes: []string{"5.44.44.0/24"}, fetched: time.Now()}
	geoCache.Unlock()
	defer func() { geoCache.Lock(); delete(geoCache.data, "by"); geoCache.Unlock() }()
	w = httptest.NewRecorder()
	handleGeoPrefixes(w, httptest.NewRequest(http.MethodPost, "/api/geo/prefixes", strings.NewReader(`{"countries":["by","by"]}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate country: %d", w.Code)
	}
}

func TestGeoNatLargeConfigRequest(t *testing.T) {
	prefixes := make([]string, 60000)
	for i := range prefixes {
		prefixes[i] = fmt.Sprintf("100.100.%d.%d/32", i/256, i%256)
	}
	request := map[string]any{
		"device":     map[string]any{"routeros": "7.15", "packages": []any{map[string]string{"name": "routeros"}}},
		"interfaces": []any{map[string]string{"name": "ether1"}},
		"settings":   map[string]any{"geoNat": map[string]any{"enabled": true, "resource": "web", "wan": "ether1", "destination": "192.168.88.10", "port": "443", "protocol": "tcp", "countries": []string{"us"}, "countryPrefixes": prefixes, "acknowledged": true}},
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) <= 1<<20 {
		t.Fatalf("test should exceed old request limit: %d", len(data))
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(data))
	handleConfig(w, req)
	var res struct {
		OK     bool   `json:"ok"`
		Script string `json:"script"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || !res.OK {
		t.Fatalf("large request: HTTP %d, %v, %s", w.Code, err, res.Error)
	}
	if !strings.Contains(res.Script, "address=100.100.234.95/32") {
		t.Fatal("missing last prefix")
	}
}
