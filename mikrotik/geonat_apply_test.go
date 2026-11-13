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
	"sync"
	"testing"
	"time"
)

func geoFixture() (GeoNATSettings, map[string][]map[string]string) {
	g := GeoNATSettings{Enabled: true, Resource: "rdp", WAN: "ether1", Destination: "192.168.88.5", Port: "3389", Protocol: "tcp", Manual: "203.0.113.1", Acknowledged: true}
	data := map[string][]map[string]string{
		"/interface":       {{"name": "ether1", "disabled": "false"}},
		"/ip/address":      {{"address": "192.168.88.1/24"}},
		"/ip/firewall/nat": {{"chain": "dstnat", "action": "dst-nat", "disabled": "false", "protocol": "tcp", "to-addresses": "192.168.88.5", "to-ports": "3389", "in-interface": "ether1"}},
	}
	return g, data
}

func TestGeoNATPreflight(t *testing.T) {
	cases := []struct {
		name   string
		change func(GeoNATSettings, map[string][]map[string]string)
		wantOK bool
	}{
		{"exact", func(_ GeoNATSettings, _ map[string][]map[string]string) {}, true},
		{"local-device", func(_ GeoNATSettings, d map[string][]map[string]string) {
			d["/ip/address"] = append(d["/ip/address"], map[string]string{"address": "192.168.88.5/24"})
		}, false},
		{"bad-address", func(_ GeoNATSettings, d map[string][]map[string]string) {
			d["/ip/address"] = []map[string]string{{"address": "broken"}}
		}, false},
		{"other-resource-conflict", func(_ GeoNATSettings, d map[string][]map[string]string) {
			d["/ip/firewall/filter"] = []map[string]string{{"comment": "mti-nat-other-allow", "chain": "forward", "in-interface": "ether1", "dst-address": "192.168.88.5", "protocol": "tcp", "dst-port": "3389"}}
		}, false},
		{"other-resource-subnet-conflict", func(_ GeoNATSettings, d map[string][]map[string]string) {
			d["/ip/firewall/filter"] = []map[string]string{{"comment": "mti-nat-other-allow", "chain": "forward", "in-interface": "ether1", "dst-address": "192.168.88.0/24", "protocol": "tcp", "dst-port": "3300-3400"}}
		}, false},
		{"own-complete", func(_ GeoNATSettings, d map[string][]map[string]string) {
			d["/ip/firewall/filter"] = []map[string]string{{"comment": "mti-nat-a0680e7d85c72ada-x-allow", "chain": "forward", "disabled": "false", "action": "accept", "in-interface": "ether1", "dst-address": "192.168.88.5", "protocol": "tcp", "dst-port": "3389"}, {"comment": "mti-nat-a0680e7d85c72ada-x-drop", "chain": "forward", "disabled": "false", "action": "drop", "in-interface": "ether1", "dst-address": "192.168.88.5", "protocol": "tcp", "dst-port": "3389"}}
		}, true},
		{"own-incomplete", func(_ GeoNATSettings, d map[string][]map[string]string) {
			d["/ip/firewall/filter"] = []map[string]string{{"comment": "mti-nat-a0680e7d85c72ada-x-allow", "chain": "forward", "disabled": "false", "action": "accept", "in-interface": "ether1", "dst-address": "192.168.88.5", "protocol": "tcp", "dst-port": "3389"}}
		}, false},
		{"no-nat", func(_ GeoNATSettings, d map[string][]map[string]string) { d["/ip/firewall/nat"] = nil }, false},
		{"disabled", func(_ GeoNATSettings, d map[string][]map[string]string) {
			d["/ip/firewall/nat"][0]["disabled"] = "true"
		}, false},
		{"wrong-wan", func(_ GeoNATSettings, d map[string][]map[string]string) {
			d["/ip/firewall/nat"][0]["in-interface"] = "ether2"
		}, false},
		{"wrong-port", func(_ GeoNATSettings, d map[string][]map[string]string) { d["/ip/firewall/nat"][0]["to-ports"] = "22" }, false},
		{"wrong-protocol", func(_ GeoNATSettings, d map[string][]map[string]string) { d["/ip/firewall/nat"][0]["protocol"] = "udp" }, false},
		{"wan-disabled", func(_ GeoNATSettings, d map[string][]map[string]string) { d["/interface"][0]["disabled"] = "true" }, false},
		{"wan-unknown", func(_ GeoNATSettings, d map[string][]map[string]string) { delete(d["/interface"][0], "disabled") }, false},
		{"nat-unknown", func(_ GeoNATSettings, d map[string][]map[string]string) { delete(d["/ip/firewall/nat"][0], "disabled") }, false},
		{"port-unchanged", func(_ GeoNATSettings, d map[string][]map[string]string) {
			delete(d["/ip/firewall/nat"][0], "to-ports")
			d["/ip/firewall/nat"][0]["dst-port"] = "3389"
		}, true},
		{"wan-list", func(_ GeoNATSettings, d map[string][]map[string]string) {
			delete(d["/ip/firewall/nat"][0], "in-interface")
			d["/ip/firewall/nat"][0]["in-interface-list"] = "WAN"
			d["/interface/list/member"] = []map[string]string{{"list": "WAN", "interface": "ether1", "disabled": "false"}}
		}, true},
		{"wan-list-other", func(_ GeoNATSettings, d map[string][]map[string]string) {
			delete(d["/ip/firewall/nat"][0], "in-interface")
			d["/ip/firewall/nat"][0]["in-interface-list"] = "WAN"
			d["/interface/list/member"] = []map[string]string{{"list": "WAN", "interface": "ether2", "disabled": "false"}}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, data := geoFixture()
			tc.change(g, data)
			read := func(path string) ([]map[string]string, error) { return data[path], nil }
			err := checkGeoNAT(g, read)
			if (err == nil) != tc.wantOK {
				t.Fatalf("err=%v", err)
			}
		})
	}
	g, data := geoFixture()
	read := func(path string) ([]map[string]string, error) {
		if path == "/ip/firewall/nat" {
			return nil, fmt.Errorf("private error")
		}
		return data[path], nil
	}
	if err := checkGeoNAT(g, read); err == nil || strings.Contains(err.Error(), "private error") {
		t.Fatalf("read error leaked or accepted: %v", err)
	}
}

func makeGeoRequest(port int) GeoApplyRequest {
	g, _ := geoFixture()
	g.Countries = []string{"by"}
	g.Manual = ""
	for i := 0; i < 106; i++ {
		g.CountryPrefixes = append(g.CountryPrefixes, fmt.Sprintf("198.18.%d.0/24", i))
	}
	device := &Info{Protocol: "rest", Identity: "router", RouterOS: "7.25.1", Architecture: "arm", Diagnostics: &IdentityDiagnostics{ResourceBoard: "hAP ac^2"}, Packages: []Package{{Name: "routeros"}}}
	ifaces := []Iface{{Name: "ether1", Type: "ether"}}
	when := time.Date(2026, 11, 13, 12, 34, 0, 123, time.UTC)
	res := Generate(device, ifaces, &Settings{GeoNAT: g}, when)
	return GeoApplyRequest{Conn: Params{Host: "127.0.0.1", RESTPort: port, User: "admin", Password: "secret", Protocol: "rest"}, Device: device, Interfaces: ifaces, Geo: g, GeneratedAt: when, Script: res.Script}
}

func TestGeoNATRESTPreflightAndApply(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity string
		nat      string
		failRun  bool
		wantErr  string
		writes   int
	}{
		{"success", "router", `[{"chain":"dstnat","action":"dst-nat","disabled":false,"invalid":false,"protocol":"tcp","to-addresses":"192.168.88.5","to-ports":"3389","in-interface":"ether1"}]`, false, "", 3},
		{"no-nat", "router", `[]`, false, "не найден", 0},
		{"identity-changed", "other", `[]`, false, "изменились", 0},
		{"run-error", "router", `[{"chain":"dstnat","action":"dst-nat","disabled":false,"protocol":"tcp","to-addresses":"192.168.88.5","to-ports":"3389","in-interface":"ether1"}]`, true, "частично", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			var written string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls = append(calls, r.Method+" "+r.URL.Path)
				mu.Unlock()
				switch r.Method + " " + r.URL.Path {
				case "GET /rest/system/identity":
					fmt.Fprintf(w, `[{"name":%q}]`, tc.identity)
				case "GET /rest/system/resource":
					fmt.Fprint(w, `[{"version":"7.25.1","board-name":"hAP ac^2","architecture-name":"arm"}]`)
				case "GET /rest/interface":
					fmt.Fprint(w, `[{"name":"ether1","disabled":false}]`)
				case "GET /rest/ip/address":
					fmt.Fprint(w, `[{"address":"192.168.88.1/24"}]`)
				case "GET /rest/ip/firewall/filter":
					fmt.Fprint(w, `[]`)
				case "GET /rest/ip/firewall/nat":
					fmt.Fprint(w, tc.nat)
				case "GET /rest/system/script":
					fmt.Fprint(w, `[]`)
				case "POST /rest/system/script/remove":
					fmt.Fprint(w, `{}`)
				case "PUT /rest/system/script":
					var x struct {
						Source string `json:"source"`
					}
					_ = json.NewDecoder(r.Body).Decode(&x)
					mu.Lock()
					written = x.Source
					mu.Unlock()
					fmt.Fprint(w, `{}`)
				case "POST /rest/system/script/run":
					if tc.failRun {
						w.WriteHeader(500)
					}
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			_, sp, _ := net.SplitHostPort(u.Host)
			port, _ := strconv.Atoi(sp)
			req := makeGeoRequest(port)
			if len(req.Script) >= maxScriptSize {
				t.Fatal("106 prefix fixture unexpectedly too large")
			}
			proto, err := ApplyGeoNAT(context.Background(), req)
			if tc.wantErr == "" {
				if err != nil || proto != "rest" {
					t.Fatalf("%s %v", proto, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q: %v", tc.wantErr, err)
			}
			mu.Lock()
			defer mu.Unlock()
			writes := 0
			for _, c := range calls {
				if !strings.HasPrefix(c, "GET ") {
					writes++
				}
			}
			if writes != tc.writes {
				t.Fatalf("writes %d, calls %v", writes, calls)
			}
			if writes > 0 && written != req.Script {
				t.Fatal("sent script differs from preview")
			}
		})
	}
}

func TestGeoNATApplyFailsClosedBeforeConnecting(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GeoApplyRequest)
		want   string
	}{
		{"injected-model", func(r *GeoApplyRequest) {
			r.Device.Model = "router\n:put 1"
			r.Script = Generate(r.Device, r.Interfaces, &Settings{GeoNAT: r.Geo}, r.GeneratedAt).Script
		}, "некорректные данные"},
		{"changed-script", func(r *GeoApplyRequest) { r.Script += "\n:put 1" }, "предпросмотр"},
		{"changed-settings", func(r *GeoApplyRequest) { r.Geo.Destination = "192.168.88.6" }, "предпросмотр"},
		{"large", func(r *GeoApplyRequest) { r.Script = strings.Repeat("x", maxScriptSize+1) }, "ограничено"},
		{"no-backup-ack", func(r *GeoApplyRequest) { r.Geo.Acknowledged = false }, "подтвердите"},
		{"no-time", func(r *GeoApplyRequest) { r.GeneratedAt = time.Time{} }, "предпросмотра"},
		{"mac", func(r *GeoApplyRequest) {
			r.Conn.Protocol = "mac"
			r.Conn.Host = "AA:BB:CC:DD:EE:FF"
			r.Device.Protocol = "mac"
		}, "MAC-Telnet"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := makeGeoRequest(1)
			tc.mutate(&r)
			_, err := ApplyGeoNAT(context.Background(), r)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestGeoNATAPISameSessionNoRetry(t *testing.T) {
	for _, tc := range []struct {
		name     string
		nat      bool
		runError bool
		writes   int
	}{
		{"success", true, false, 3}, {"no-nat", false, false, 0}, {"run-error", true, true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			commands := make(chan []string, 1)
			go func() {
				conn, e := ln.Accept()
				if e != nil {
					commands <- nil
					return
				}
				defer conn.Close()
				var got []string
				defer func() { commands <- got }()
				for {
					words, e := readSentence(conn)
					if e != nil || len(words) == 0 {
						return
					}
					got = append(got, words[0])
					switch words[0] {
					case "/login":
						_ = writeSentence(conn, "!done")
					case "/system/identity/print":
						_ = writeSentence(conn, "!re", "=name=router")
						_ = writeSentence(conn, "!done")
					case "/system/resource/print":
						_ = writeSentence(conn, "!re", "=version=7.25.1", "=board-name=hAP ac^2", "=architecture-name=arm")
						_ = writeSentence(conn, "!done")
					case "/interface/print":
						_ = writeSentence(conn, "!re", "=name=ether1", "=disabled=false")
						_ = writeSentence(conn, "!done")
					case "/ip/address/print":
						_ = writeSentence(conn, "!re", "=address=192.168.88.1/24")
						_ = writeSentence(conn, "!done")
					case "/ip/firewall/filter/print":
						_ = writeSentence(conn, "!done")
					case "/ip/firewall/nat/print":
						if tc.nat {
							_ = writeSentence(conn, "!re", "=chain=dstnat", "=action=dst-nat", "=disabled=false", "=protocol=tcp", "=to-addresses=192.168.88.5", "=to-ports=3389", "=in-interface=ether1")
						}
						_ = writeSentence(conn, "!done")
					case "/system/script/print", "/system/script/remove", "/system/script/add":
						_ = writeSentence(conn, "!done")
					case "/system/script/run":
						if tc.runError {
							_ = writeSentence(conn, "!trap", "=message=execution failed")
						}
						_ = writeSentence(conn, "!done")
					default:
						_ = writeSentence(conn, "!trap", "=message=unexpected")
						_ = writeSentence(conn, "!done")
					}
				}
			}()
			req := makeGeoRequest(0)
			req.Conn.Protocol = "api"
			req.Conn.APIPort = ln.Addr().(*net.TCPAddr).Port
			req.Device.Protocol = "api"
			// Protocol is not used in script header; regenerate to preserve exact preview.
			req.Script = Generate(req.Device, req.Interfaces, &Settings{GeoNAT: req.Geo}, req.GeneratedAt).Script
			proto, err := ApplyGeoNAT(context.Background(), req)
			if tc.nat && !tc.runError {
				if err != nil || proto != "api" {
					t.Fatalf("%s %v", proto, err)
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
			if tc.runError && !strings.Contains(err.Error(), "частично") {
				t.Fatalf("%v", err)
			}
			got := <-commands
			writes := 0
			for _, c := range got {
				if c == "/system/script/remove" || c == "/system/script/add" || c == "/system/script/run" {
					writes++
				}
			}
			if writes != tc.writes {
				t.Fatalf("writes=%d commands=%v", writes, got)
			}
		})
	}
}
