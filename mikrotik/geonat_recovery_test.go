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
)

const recoveryTag = "mti-nat-a7e2d26e8d15814d-7b0db52842"

func recoveryFixture() (GeoRecoveryRequest, map[string][]map[string]string) {
	req := GeoRecoveryRequest{Conn: Params{Host: "127.0.0.1", User: "admin", Protocol: "api"}, Device: &Info{Protocol: "api", Identity: "router", RouterOS: "7.25.1", Architecture: "arm"}, Geo: GeoNATSettings{Enabled: true, Resource: "by", WAN: "ether1", Destination: "192.168.88.50", Port: "3389", Protocol: "tcp"}, Acknowledged: true}
	rows := map[string][]map[string]string{
		"/system/identity": {{"name": "router"}}, "/system/resource": {{"version": "7.25.1", "architecture-name": "arm"}},
		"/ip/firewall/filter": {{".id": "*A", "comment": recoveryTag + "-allow", "chain": "forward", "action": "accept", "connection-nat-state": "dstnat", "protocol": "tcp", "dst-address": "192.168.88.50", "dst-port": "3389", "in-interface": "ether1", "src-address-list": recoveryTag}, {".id": "*B", "comment": "defconf", "chain": "forward", "action": "passthrough"}},
		"/ip/firewall/nat":    {{".id": "*C"}}, "/ip/firewall/mangle": {}, "/ip/firewall/raw": {},
	}
	for i := 0; i < 106; i++ {
		rows["/ip/firewall/address-list"] = append(rows["/ip/firewall/address-list"], map[string]string{".id": fmt.Sprintf("*%X", i+16), "list": recoveryTag, "comment": recoveryTag, "dynamic": "false"})
	}
	return req, rows
}
func TestGeoRecoveryPlan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*GeoRecoveryRequest, map[string][]map[string]string)
		ok     bool
	}{
		{"user-partial", func(*GeoRecoveryRequest, map[string][]map[string]string) {}, true},
		{"other-resource", func(r *GeoRecoveryRequest, _ map[string][]map[string]string) { r.Geo.Resource = "other" }, false},
		{"address-cidr-32", func(_ *GeoRecoveryRequest, d map[string][]map[string]string) {
			d["/ip/firewall/filter"][0]["dst-address"] = "192.168.88.50/32"
		}, true},
		{"address-subnet", func(_ *GeoRecoveryRequest, d map[string][]map[string]string) {
			d["/ip/firewall/filter"][0]["dst-address"] = "192.168.88.0/24"
		}, false},
		{"target-mismatch", func(r *GeoRecoveryRequest, _ map[string][]map[string]string) { r.Geo.Destination = "192.168.88.51" }, false},
		{"complete-set", func(_ *GeoRecoveryRequest, d map[string][]map[string]string) {
			d["/ip/firewall/filter"] = append(d["/ip/firewall/filter"], map[string]string{".id": "*D", "comment": recoveryTag + "-drop"})
		}, false},
		{"unrelated-reference", func(_ *GeoRecoveryRequest, d map[string][]map[string]string) {
			d["/ip/firewall/raw"] = append(d["/ip/firewall/raw"], map[string]string{"src-address-list": "!" + recoveryTag})
		}, false},
		{"duplicate-filter-id", func(_ *GeoRecoveryRequest, d map[string][]map[string]string) {
			d["/ip/firewall/filter"][1][".id"] = "*A"
		}, false},
		{"alien-entry", func(_ *GeoRecoveryRequest, d map[string][]map[string]string) {
			d["/ip/firewall/address-list"][0]["comment"] = "alien"
		}, false},
		{"duplicate-id", func(_ *GeoRecoveryRequest, d map[string][]map[string]string) {
			d["/ip/firewall/address-list"][0][".id"] = d["/ip/firewall/address-list"][1][".id"]
		}, false},
		{"dynamic-entry", func(_ *GeoRecoveryRequest, d map[string][]map[string]string) {
			d["/ip/firewall/address-list"][0]["dynamic"] = "true"
		}, false},
		{"empty", func(_ *GeoRecoveryRequest, d map[string][]map[string]string) { d["/ip/firewall/address-list"] = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, d := recoveryFixture()
			tc.change(&r, d)
			p, err := planGeoRecovery(r.Geo, func(path string) ([]map[string]string, error) { return d[path], nil })
			if (err == nil) != tc.ok {
				t.Fatalf("%+v %v", p, err)
			}
			if tc.ok && (p.filterID != "*A" || len(p.addressIDs) != 106 || p.tag != recoveryTag) {
				t.Fatalf("%+v", p)
			}
		})
	}
}
func TestGeoRecoveryRESTEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name           string
		conflict, fail bool
		writes         int
	}{{"success", false, false, 107}, {"reference", true, false, 0}, {"partial-failure", false, true, 2}, {"race", false, false, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			req, data := recoveryFixture()
			req.Conn.Protocol = "rest"
			req.Device.Protocol = "rest"
			if tc.conflict {
				data["/ip/firewall/nat"] = append(data["/ip/firewall/nat"], map[string]string{"src-address-list": recoveryTag})
			}
			var mu sync.Mutex
			writes := 0
			natReads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				path := strings.TrimPrefix(r.URL.Path, "/rest")
				if r.Method == "GET" {
					props, ok := geoRecoveryProperties[path]
					if !ok || r.URL.Query().Get(".proplist") != props {
						t.Errorf("unexpected read %s %s", path, r.URL.RawQuery)
						w.WriteHeader(500)
						return
					}
					if path == "/ip/firewall/nat" {
						natReads++
						if tc.name == "race" && natReads == 2 {
							data[path] = append(data[path], map[string]string{"src-address-list": recoveryTag})
						}
					}
					_ = json.NewEncoder(w).Encode(data[path])
					return
				}
				if r.Method != "POST" || !strings.HasSuffix(path, "/remove") {
					t.Errorf("unexpected write %s %s", r.Method, path)
					w.WriteHeader(500)
					return
				}
				path = strings.TrimSuffix(path, "/remove")
				if path != "/ip/firewall/filter" && path != "/ip/firewall/address-list" {
					t.Errorf("wrong removal %s", path)
					w.WriteHeader(500)
					return
				}
				writes++
				if tc.fail && writes == 2 {
					w.WriteHeader(500)
					return
				}
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				next := []map[string]string{}
				for _, x := range data[path] {
					if x[".id"] != body["numbers"] {
						next = append(next, x)
					}
				}
				data[path] = next
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			_, p, _ := net.SplitHostPort(u.Host)
			req.Conn.RESTPort, _ = strconv.Atoi(p)
			count, err := RecoverGeoNAT(context.Background(), req)
			if tc.name == "success" {
				if err != nil || count != 106 {
					t.Fatalf("count %d %v", count, err)
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
			if tc.fail && !strings.Contains(err.Error(), "частич") {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if writes != tc.writes {
				t.Fatalf("writes=%d", writes)
			}
		})
	}
}
func TestGeoRecoveryAPIEndToEnd(t *testing.T) {
	req, data := recoveryFixture()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	req.Conn.APIPort = ln.Addr().(*net.TCPAddr).Port
	commands := make(chan []string, 1)
	go func() {
		conn, e := ln.Accept()
		if e != nil {
			commands <- nil
			return
		}
		defer conn.Close()
		var calls []string
		defer func() { commands <- calls }()
		for {
			words, e := readSentence(conn)
			if e != nil {
				return
			}
			calls = append(calls, words[0])
			if words[0] == "/login" {
				_ = writeSentence(conn, "!done")
				continue
			}
			if strings.HasSuffix(words[0], "/print") {
				path := strings.TrimSuffix(words[0], "/print")
				if len(words) != 2 || words[1] != "=.proplist="+geoRecoveryProperties[path] {
					t.Errorf("bad read %v", words)
					return
				}
				for _, row := range data[path] {
					reply := []string{"!re"}
					for k, v := range row {
						reply = append(reply, "="+k+"="+v)
					}
					_ = writeSentence(conn, reply...)
				}
				_ = writeSentence(conn, "!done")
				continue
			}
			if strings.HasSuffix(words[0], "/remove") {
				path := strings.TrimSuffix(words[0], "/remove")
				if path != "/ip/firewall/filter" && path != "/ip/firewall/address-list" {
					t.Errorf("unexpected remove %s", path)
					return
				}
				id := strings.TrimPrefix(words[1], "=numbers=")
				out := []map[string]string{}
				for _, r := range data[path] {
					if r[".id"] != id {
						out = append(out, r)
					}
				}
				data[path] = out
				_ = writeSentence(conn, "!done")
				continue
			}
			t.Errorf("unexpected %v", words)
			return
		}
	}()
	n, err := RecoverGeoNAT(context.Background(), req)
	if err != nil || n != 106 {
		t.Fatalf("%d %v", n, err)
	}
	calls := <-commands
	writes := 0
	for _, c := range calls {
		if strings.HasSuffix(c, "/remove") {
			writes++
		}
		if c == "/ip/firewall/nat/remove" {
			t.Fatal("removed NAT")
		}
	}
	if writes != 107 {
		t.Fatalf("writes %d", writes)
	}
}
