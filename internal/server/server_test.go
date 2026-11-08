package server

import (
	"bytes"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zigorminsk-debug/my-mikrotik/web"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(NewMux(Options{Version: "test-1.2.3", WebRoot: web.Content()}))
	t.Cleanup(srv.Close)
	return srv
}

func TestIndexServed(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("статус %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"Конструктор", "manifest.webmanifest", "/icon.png"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("в странице нет %q", want)
		}
	}
	if got := resp.Header.Get("Content-Security-Policy"); got == "" {
		t.Error("нет заголовка Content-Security-Policy")
	}
}

func TestIconsAndManifest(t *testing.T) {
	srv := newTestServer(t)

	for _, path := range []string{"/icon.png", "/icon-192.png", "/icon-512.png"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: статус %d", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
			t.Errorf("%s: Content-Type = %q", path, ct)
		}
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			t.Errorf("%s: не PNG: %v", path, err)
		}
	}

	resp, err := http.Get(srv.URL + "/manifest.webmanifest")
	if err != nil {
		t.Fatalf("манифест: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("манифест не JSON: %v", err)
	}
	if m["name"] != "MikroTik Inspector" {
		t.Errorf("name = %v", m["name"])
	}
	if icons, ok := m["icons"].([]any); !ok || len(icons) < 2 {
		t.Errorf("icons = %v", m["icons"])
	}
}

func TestIconSizes(t *testing.T) {
	for _, size := range []int{48, 192, 512} {
		img, err := png.Decode(bytes.NewReader(IconPNG(size)))
		if err != nil {
			t.Fatalf("%d: %v", size, err)
		}
		b := img.Bounds()
		if b.Dx() != size || b.Dy() != size {
			t.Errorf("%d: размер %dx%d", size, b.Dx(), b.Dy())
		}
	}
}

func TestApiErrors(t *testing.T) {
	srv := newTestServer(t)

	post := func(path, body string) (int, map[string]any) {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}

	// GET вместо POST — 405.
	resp, err := http.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/config: статус %d", resp.StatusCode)
	}

	// Без устройства — 400 с понятным текстом.
	code, m := post("/api/config", `{"device":null}`)
	if code != http.StatusBadRequest || m["ok"] != false {
		t.Errorf("config без устройства: %d %v", code, m)
	}

	// Недоступный роутер — ok:false, но HTTP 200 (ошибка подключения, не запроса).
	code, m = post("/api/info", `{"host":"127.0.0.1","port":1,"user":"admin","protocol":"api"}`)
	if code != http.StatusOK || m["ok"] != false {
		t.Errorf("info на недоступный хост: %d %v", code, m)
	}
	if errText, _ := m["error"].(string); !strings.Contains(errText, "127.0.0.1") {
		t.Errorf("error = %v", m["error"])
	}

	// Применение пустого скрипта — ошибка валидации.
	code, m = post("/api/apply", `{"conn":{"host":"127.0.0.1"},"script":"  "}`)
	if code != http.StatusOK || m["ok"] != false {
		t.Errorf("apply пустого скрипта: %d %v", code, m)
	}
}

// Сквозной сценарий конструктора через HTTP: генерация скрипта под устройство.
func TestConfigOverHTTP(t *testing.T) {
	srv := newTestServer(t)
	reqBody := `{
	  "device": {"model":"hEX S","routeros":"7.15.3 (stable)",
	    "packages":[{"name":"system","version":"7.15.3"},{"name":"ppp","version":"7.15.3"}]},
	  "interfaces": [{"name":"ether1","type":"ethernet"},{"name":"ether2","type":"ethernet"}],
	  "settings": {
	    "lan":  {"enabled":true,"iface":"ether2","address":"192.168.088.010/24"},
	    "dhcp": {"enabled":true,"iface":"ether2","leaseTime":"1d"},
	    "pppoe":{"enabled":true,"iface":"ether1","name":"pppoe-out1","user":"u","password":"p","defaultRoute":true,"nat":true}
	  }
	}`
	resp, err := http.Post(srv.URL+"/api/config", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /api/config: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("статус %d", resp.StatusCode)
	}
	var res struct {
		OK      bool   `json:"ok"`
		Script  string `json:"script"`
		Error   string `json:"error"`
		Derived struct {
			Network string `json:"network"`
			Gateway string `json:"gateway"`
			Pool    string `json:"pool"`
		} `json:"derived"`
		Skipped []struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("ok=false: %s", res.Error)
	}
	// Адрес приведён к формату, DHCP пропущен (пакета dhcp нет).
	if !strings.Contains(res.Script, "address=192.168.88.10/24") {
		t.Errorf("нет нормализованного адреса:\n%s", res.Script)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].ID != "dhcp" ||
		!strings.Contains(res.Skipped[0].Reason, "необходимый пакет отсутствует") {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	if res.Derived.Network != "192.168.88.0/24" || res.Derived.Gateway != "192.168.88.10" {
		t.Errorf("derived = %+v", res.Derived)
	}
}

func TestLocalURLs(t *testing.T) {
	for _, u := range LocalURLs(8765) {
		if !strings.HasPrefix(u, "http://") || !strings.HasSuffix(u, ":8765") {
			t.Errorf("адрес %q", u)
		}
	}
}
