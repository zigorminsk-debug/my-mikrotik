package mikrotik

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestCollectLTEStatesAndPrivacy(t *testing.T) {
	read := func(string) ([]map[string]string, error) {
		return []map[string]string{{"name": "lte1", "running": "true", "disabled": "false", "imei": "SECRET"}, {"name": "lte2", "disabled": "true"}, {"name": "lte3"}, {"name": "lte4"}, {"name": "lte5"}}, nil
	}
	var calls []string
	monitor := func(name string) ([]map[string]string, error) {
		calls = append(calls, name)
		if name == "lte3" {
			return nil, fmt.Errorf("SECRET")
		}
		return []map[string]string{{"rssi": "-68dBm", "rsrp": "-97dBm", "rsrq": "-9dB", "sinr": "6dB", "cqi": "7", "data-class": "LTE", "imei": "SECRET", "imsi": "SECRET", "iccid": "SECRET", "current-cellid": "SECRET", "current-operator": "SECRET", "status": "connected"}}, nil
	}
	h := collectLTE(context.Background(), read, monitor, false)
	if h.Status != "ok" || !h.Truncated || len(h.Rows) != 4 || h.Rows[0]["rsrp"] != "-97dBm" || h.Rows[0]["monitor-status"] != "ok" || h.Rows[1]["monitor-status"] != "skipped" || h.Rows[2]["monitor-status"] != "unavailable" || h.Rows[3]["monitor-status"] != "ok" {
		t.Fatalf("%+v", h)
	}
	if strings.Join(calls, ",") != "lte1,lte3,lte4" {
		t.Fatal(calls)
	}
	b, _ := json.Marshal(h)
	if strings.Contains(string(b), "SECRET") {
		t.Fatal(string(b))
	}
	calls = nil
	h = collectLTE(context.Background(), read, monitor, true)
	if h.Rows[3]["monitor-status"] != "skipped" || strings.Join(calls, ",") != "lte1,lte3" {
		t.Fatalf("%+v %v", h, calls)
	}
	empty := func(string) ([]map[string]string, error) { return nil, nil }
	h = collectLTE(context.Background(), empty, monitor, false)
	if h.Status != "ok" || len(h.Rows) != 0 {
		t.Fatalf("%+v", h)
	}
	fail := func(string) ([]map[string]string, error) { return nil, fmt.Errorf("SECRET") }
	h = collectLTE(context.Background(), fail, monitor, false)
	if h.Status != "error" || len(h.Rows) != 0 {
		t.Fatalf("%+v", h)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h = collectLTE(ctx, read, monitor, false)
	if h.Status != "skipped" {
		t.Fatal(h)
	}
	if safeLTEValue("foo\nSECRET") != "" || safeLTEValue(strings.Repeat("a", 129)) != "" {
		t.Fatal("unsafe LTE value accepted")
	}
}

func TestCollectLTESnapshotRESTOptIn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		version  string
		opt      bool
		response string
		want     string
		calls    int
	}{
		{"opt-out", "7.15", false, "", "absent", 0},
		{"object", "7.15", true, `{"status":"connected","rsrp":"-97dBm","imei":"SECRET","imsi":"SECRET","current-cellid":"SECRET"}`, "ok", 1},
		{"array", "7.15", true, `[{"rssi":"-68dBm","iccid":"SECRET"}]`, "ok", 1},
		{"empty", "7.15", true, `[]`, "unavailable", 1},
		{"ros6", "6.49", true, "", "unsupported", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				switch r.URL.Path {
				case "/rest/system/identity":
					fmt.Fprint(w, `[{"name":"router"}]`)
				case "/rest/system/resource":
					fmt.Fprintf(w, `[{"version":%q,"architecture-name":"arm","board-name":"hAP"}]`, tc.version)
				case "/rest/interface/lte":
					if r.URL.Query().Get(".proplist") != "name,running,disabled" {
						t.Errorf("proplist: %s", r.URL.RawQuery)
					}
					fmt.Fprint(w, `[{"name":"lte1","disabled":false,"running":true,"imei":"SECRET"}]`)
				case "/rest/interface/lte/monitor":
					if r.Method != "POST" {
						t.Errorf("method %s", r.Method)
					}
					var b map[string]string
					_ = json.NewDecoder(r.Body).Decode(&b)
					if b["numbers"] != "lte1" || b["once"] != "" || b[".proplist"] != lteMonitorProperties {
						t.Errorf("body=%v", b)
					}
					fmt.Fprint(w, tc.response)
				default:
					fmt.Fprint(w, `[]`)
				}
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			_, ps, _ := net.SplitHostPort(u.Host)
			port, _ := strconv.Atoi(ps)
			p := Params{Host: "127.0.0.1", RESTPort: port, User: "admin", Protocol: "rest"}
			device := &Info{Protocol: "rest", Identity: "router", RouterOS: tc.version, Architecture: "arm", Diagnostics: &IdentityDiagnostics{ResourceBoard: "hAP"}}
			snap, err := CollectNetworkSnapshotWithOptions(context.Background(), p, device, NetworkSnapshotOptions{IncludeLTE: tc.opt})
			if err != nil {
				t.Fatal(err)
			}
			h, ok := snap.Sections["lte"]
			if tc.want == "absent" {
				if ok {
					t.Fatal(h)
				}
			} else if tc.want == "unsupported" {
				if h.Status != "unsupported" {
					t.Fatal(h)
				}
			} else if h.Status != "ok" || len(h.Rows) != 1 || h.Rows[0]["monitor-status"] != tc.want {
				t.Fatalf("%+v", h)
			}
			count := 0
			for _, c := range calls {
				if c == "POST /rest/interface/lte/monitor" {
					count++
				} else if !strings.HasPrefix(c, "GET ") {
					t.Fatalf("unexpected request %s", c)
				}
			}
			if count != tc.calls {
				t.Fatalf("%v", calls)
			}
			encoded, _ := json.Marshal(snap)
			if strings.Contains(string(encoded), "SECRET") {
				t.Fatal(string(encoded))
			}
		})
	}
}

func TestCollectLTESnapshotAPI(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan [][]string, 1)
	go func() {
		conn, e := ln.Accept()
		if e != nil {
			got <- nil
			return
		}
		defer conn.Close()
		var cmds [][]string
		defer func() { got <- cmds }()
		for {
			s, e := readSentence(conn)
			if e != nil || len(s) == 0 {
				return
			}
			cmds = append(cmds, s)
			switch s[0] {
			case "/system/identity/print":
				_ = writeSentence(conn, "!re", "=name=router")
			case "/system/resource/print":
				_ = writeSentence(conn, "!re", "=version=7.15", "=board-name=hAP", "=architecture-name=arm")
			case "/interface/lte/print":
				_ = writeSentence(conn, "!re", "=name=lte1", "=disabled=false", "=running=true", "=imei=SECRET")
			case "/interface/lte/monitor":
				_ = writeSentence(conn, "!re", "=rsrp=-97dBm", "=imei=SECRET")
			}
			_ = writeSentence(conn, "!done")
		}
	}()
	p := Params{Host: "127.0.0.1", APIPort: ln.Addr().(*net.TCPAddr).Port, User: "admin", Protocol: "api"}
	device := &Info{Protocol: "api", Identity: "router", RouterOS: "7.15", Architecture: "arm", Diagnostics: &IdentityDiagnostics{ResourceBoard: "hAP"}}
	snap, err := CollectNetworkSnapshotWithOptions(context.Background(), p, device, NetworkSnapshotOptions{IncludeLTE: true})
	if err != nil {
		t.Fatal(err)
	}
	h := snap.Sections["lte"]
	if h.Status != "ok" || len(h.Rows) != 1 || h.Rows[0]["rsrp"] != "-97dBm" {
		t.Fatal(h)
	}
	cmds := <-got
	for _, cmd := range cmds {
		if cmd[0] != "/login" && !strings.HasSuffix(cmd[0], "/print") && cmd[0] != "/interface/lte/monitor" {
			t.Fatalf("unexpected %v", cmd)
		}
	}
	last := cmds[len(cmds)-1]
	if strings.Join(last, "|") != "/interface/lte/monitor|=numbers=lte1|=once=|=.proplist="+lteMonitorProperties {
		t.Fatalf("%v", last)
	}
	b, _ := json.Marshal(snap)
	if strings.Contains(string(b), "SECRET") {
		t.Fatal(string(b))
	}
}
