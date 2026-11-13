package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
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

func TestAPIRejectsCrossOriginAndNonJSONPosts(t *testing.T) {
	srv := newTestServer(t)
	for _, tc := range []struct {
		name, contentType, origin string
		want                      int
	}{
		{"html form", "text/plain", "https://evil.example", http.StatusUnsupportedMediaType},
		{"cross origin json", "application/json", "https://evil.example", http.StatusForbidden},
		{"null origin", "application/json", "null", http.StatusForbidden},
		{"same origin json", "application/json; charset=utf-8", srv.URL, http.StatusOK},
		{"native client json", "application/json", "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/apply", strings.NewReader(`{"conn":{},"script":""}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", tc.contentType)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
		})
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

func TestListenAndServeUntilWindowCloses(t *testing.T) {
	done := make(chan error, 1)
	addr := make(chan string, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- ListenAndServeUntil("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }), func(a string) error { addr <- a; return nil }, func() <-chan error { return done })
	}()
	a := <-addr
	resp, err := http.Get("http://" + a)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	done <- nil
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	client := &http.Client{Timeout: time.Second}
	if resp, err := client.Get("http://" + a); err == nil {
		resp.Body.Close()
		t.Fatal("server still listening")
	}
}

func TestServerReadyBeforeWindowLaunch(t *testing.T) {
	done := make(chan error, 1)
	finished := make(chan error, 1)
	gotReady := make(chan error, 1)
	go func() {
		finished <- ListenAndServeUntil("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ready")) }), func(addr string) error {
			client := http.Client{Timeout: 2 * time.Second}
			res, err := client.Get("http://" + addr)
			if err != nil {
				gotReady <- err
				return err
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err == nil && string(body) != "ready" {
				err = fmt.Errorf("unexpected body %q", body)
			}
			gotReady <- err
			return err
		}, func() <-chan error { return done })
	}()
	if err := <-gotReady; err != nil {
		t.Fatal(err)
	}
	done <- nil
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestServerClosesWhenReadyCallbackFails(t *testing.T) {
	var addr string
	err := ListenAndServeUntil("127.0.0.1:0", http.NotFoundHandler(), func(a string) error { addr = a; return fmt.Errorf("startup failed") }, nil)
	if err == nil || err.Error() != "startup failed" {
		t.Fatalf("unexpected error: %v", err)
	}
	client := http.Client{Timeout: time.Second}
	if resp, err := client.Get("http://" + addr); err == nil {
		resp.Body.Close()
		t.Fatal("listener still active")
	}
}

func TestVersionAvailableWithoutRouter(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control = %q", got)
	}
	var body struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Version != "test-1.2.3" {
		t.Fatalf("unexpected response: %+v", body)
	}
	post, err := http.Post(srv.URL+"/api/version", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer post.Body.Close()
	if post.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status %d", post.StatusCode)
	}
}

func TestNetworkSnapshotEndpointValidation(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Post(srv.URL+"/api/network-snapshot", "application/json", strings.NewReader(`{"conn":{"host":"127.0.0.1","user":"admin","protocol":"rest"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || body.OK || !strings.Contains(body.Error, "подключитесь") {
		t.Fatalf("status=%d body=%+v", resp.StatusCode, body)
	}
}

func TestNetworkSnapshotOverHTTP(t *testing.T) {
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		switch r.URL.Path {
		case "/rest/system/identity":
			io.WriteString(w, `[{"name":"router"}]`)
		case "/rest/system/resource":
			io.WriteString(w, `[{"board-name":"hAP","version":"7.15","architecture-name":"arm"}]`)
		case "/rest/interface":
			io.WriteString(w, `[{"name":"ether1","running":"true"}]`)
		default:
			io.WriteString(w, `[]`)
		}
	}))
	defer router.Close()
	hostport := strings.TrimPrefix(router.URL, "http://")
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	req := map[string]any{"conn": map[string]any{"host": host, "restPort": port, "user": "admin", "password": "topsecret", "protocol": "rest"}, "device": map[string]any{"protocol": "rest", "identity": "router", "routeros": "7.15", "architecture": "arm", "diagnostics": map[string]any{"resourceBoard": "hAP"}}}
	b, _ := json.Marshal(req)
	srv := newTestServer(t)
	resp, err := http.Post(srv.URL+"/api/network-snapshot", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"name":"ether1"`) || strings.Contains(string(raw), "topsecret") {
		t.Fatalf("response: %d %s", resp.StatusCode, raw)
	}
}

func TestGeoNATApplyHTTPPreviewAndGuard(t *testing.T) {
	var requests []string
	var saved string
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /rest/system/identity":
			fmt.Fprint(w, `[{"name":"router"}]`)
		case "GET /rest/system/resource":
			fmt.Fprint(w, `[{"version":"7.25.1","board-name":"hAP ac^2","architecture-name":"arm"}]`)
		case "GET /rest/interface":
			fmt.Fprint(w, `[{"name":"ether1","disabled":false}]`)
		case "GET /rest/ip/address":
			fmt.Fprint(w, `[{"address":"192.168.88.1/24"}]`)
		case "GET /rest/ip/firewall/filter":
			fmt.Fprint(w, `[]`)
		case "GET /rest/ip/firewall/nat":
			fmt.Fprint(w, `[{"chain":"dstnat","action":"dst-nat","disabled":false,"protocol":"tcp","to-addresses":"192.168.88.5","to-ports":"3389","in-interface":"ether1"}]`)
		case "GET /rest/system/script":
			fmt.Fprint(w, `[]`)
		case "POST /rest/system/script/remove", "POST /rest/system/script/run":
			fmt.Fprint(w, `{}`)
		case "PUT /rest/system/script":
			var body struct {
				Source string `json:"source"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			saved = body.Source
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer router.Close()
	u, _ := url.Parse(router.URL)
	_, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)
	dev := &mikrotik.Info{Protocol: "rest", Identity: "router", RouterOS: "7.25.1", Architecture: "arm", Diagnostics: &mikrotik.IdentityDiagnostics{ResourceBoard: "hAP ac^2"}, Packages: []mikrotik.Package{{Name: "routeros"}}}
	ifs := []mikrotik.Iface{{Name: "ether1", Type: "ether"}}
	geo := mikrotik.GeoNATSettings{Enabled: true, Resource: "rdp", WAN: "ether1", Destination: "192.168.88.5", Port: "3389", Protocol: "tcp", Manual: "203.0.113.1", Acknowledged: true}
	app := newTestServer(t)
	post := func(path string, body any, out any) {
		t.Helper()
		b, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		resp, e := http.Post(app.URL+path, "application/json", bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		if e = json.NewDecoder(resp.Body).Decode(out); e != nil {
			t.Fatal(e)
		}
	}
	var preview mikrotik.ConfigResult
	post("/api/config", mikrotik.ConfigRequest{Device: dev, Interfaces: ifs, Settings: mikrotik.Settings{GeoNAT: geo}}, &preview)
	if !preview.OK || preview.GeneratedAt.IsZero() {
		t.Fatalf("preview: %+v", preview)
	}
	req := mikrotik.GeoApplyRequest{Conn: mikrotik.Params{Host: "127.0.0.1", RESTPort: port, User: "admin", Protocol: "rest"}, Device: dev, Interfaces: ifs, Geo: geo, GeneratedAt: preview.GeneratedAt, Script: preview.Script}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	post("/api/geonat/apply", req, &out)
	if !out.OK || saved != preview.Script {
		t.Fatalf("apply=%+v, script match=%t; requests=%v", out, saved == preview.Script, requests)
	}
	before := len(requests)
	req.Script += "\n:put 1"
	out.OK = false
	post("/api/geonat/apply", req, &out)
	if out.OK || !strings.Contains(out.Error, "предпросмотр") || len(requests) != before {
		t.Fatalf("changed preview not blocked: %+v %v", out, requests[before:])
	}
}

func TestOverlappingApplyIsRejected(t *testing.T) {
	srv := newTestServer(t)
	applyGate.Lock()
	defer applyGate.Unlock()
	for _, path := range []string{"/api/apply", "/api/geonat/apply"} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		err = json.NewDecoder(resp.Body).Decode(&m)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if m["ok"] != false || !strings.Contains(fmt.Sprint(m["error"]), "другое применение") {
			t.Fatalf("%s: %v", path, m)
		}
	}
}

func TestSnapshotHistoryOptInHTTP(t *testing.T) {
	logReads := 0
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/system/identity":
			fmt.Fprint(w, `[{"name":"router"}]`)
		case "/rest/system/resource":
			fmt.Fprint(w, `[{"version":"7.15","board-name":"hAP","architecture-name":"arm"}]`)
		case "/rest/log":
			logReads++
			fmt.Fprint(w, `[{"time":"11:57:00","message":"mti-failover: backup=true, приоритет основного канала 3"},{"message":"SECRET"}]`)
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	defer router.Close()
	u, _ := url.Parse(router.URL)
	_, ps, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(ps)
	app := newTestServer(t)
	body := map[string]any{
		"conn":   mikrotik.Params{Host: "127.0.0.1", RESTPort: port, User: "admin", Protocol: "rest"},
		"device": &mikrotik.Info{Identity: "router", RouterOS: "7.15", Protocol: "rest", Architecture: "arm", Diagnostics: &mikrotik.IdentityDiagnostics{ResourceBoard: "hAP"}},
	}
	for _, requested := range []bool{false, true} {
		body["includeHistory"] = requested
		b, _ := json.Marshal(body)
		resp, err := http.Post(app.URL+"/api/network-snapshot", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			OK       bool                      `json:"ok"`
			Snapshot *mikrotik.NetworkSnapshot `json:"snapshot"`
		}
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil || !result.OK || result.Snapshot == nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		h, present := result.Snapshot.Sections["failoverHistory"]
		if present != requested {
			t.Fatalf("requested=%v section=%+v", requested, h)
		}
		if requested && (logReads != 1 || len(h.Rows) != 1 || h.Rows[0]["backup"] != "true") {
			t.Fatalf("reads=%d %+v", logReads, h)
		}
		if !requested && logReads != 0 {
			t.Fatal("log read without opt in")
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "SECRET") {
			t.Fatal("raw log exposed")
		}
	}
}

func TestSnapshotLTEOptInHTTP(t *testing.T) {
	monitorCalls := 0
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/system/identity":
			fmt.Fprint(w, `[{"name":"router"}]`)
		case "/rest/system/resource":
			fmt.Fprint(w, `[{"version":"7.15","board-name":"hAP","architecture-name":"arm"}]`)
		case "/rest/interface/lte":
			fmt.Fprint(w, `[{"name":"lte1","disabled":false,"running":true}]`)
		case "/rest/interface/lte/monitor":
			monitorCalls++
			fmt.Fprint(w, `[{"rsrp":"-97dBm","imsi":"SECRET"}]`)
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	defer router.Close()
	u, _ := url.Parse(router.URL)
	_, ps, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(ps)
	app := newTestServer(t)
	body := map[string]any{"conn": mikrotik.Params{Host: "127.0.0.1", RESTPort: port, User: "admin", Protocol: "rest"}, "device": &mikrotik.Info{Identity: "router", RouterOS: "7.15", Protocol: "rest", Architecture: "arm", Diagnostics: &mikrotik.IdentityDiagnostics{ResourceBoard: "hAP"}}}
	for _, requested := range []bool{false, true} {
		body["includeLTE"] = requested
		b, _ := json.Marshal(body)
		resp, err := http.Post(app.URL+"/api/network-snapshot", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			OK       bool                      `json:"ok"`
			Snapshot *mikrotik.NetworkSnapshot `json:"snapshot"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil || !out.OK || out.Snapshot == nil {
			t.Fatalf("%+v %v", out, err)
		}
		h, present := out.Snapshot.Sections["lte"]
		if present != requested {
			t.Fatalf("present=%v %+v", present, h)
		}
		if requested && (monitorCalls != 1 || len(h.Rows) != 1 || h.Rows[0]["rsrp"] != "-97dBm") {
			t.Fatalf("%d %+v", monitorCalls, h)
		}
		if !requested && monitorCalls != 0 {
			t.Fatal("LTE monitor without opt in")
		}
		encoded, _ := json.Marshal(out)
		if strings.Contains(string(encoded), "SECRET") {
			t.Fatal("sensitive fields leaked")
		}
	}
}
