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

func TestSnapshotFilterAndPartialResults(t *testing.T) {
	read := func(path string) ([]map[string]string, error) {
		switch path {
		case "/interface":
			return []map[string]string{{"name": "ether1", "running": "true", "secret": "PASSWORD"}}, nil
		case "/ip/address":
			return nil, fmt.Errorf("permission denied: SECRET")
		case "/ip/dhcp-client":
			return []map[string]string{}, nil
		case "/ip/route":
			return []map[string]string{
				{"dst-address": "0.0.0.0/0", "gateway": "192.0.2.1", "active": "true"},
				{"dst-address": "192.168.0.0/16", "gateway": "192.0.2.2"},
				{"gateway": "unknown"},
				{"dst-address": "1.1.1.1/32", "comment": "mti-probe-link"},
			}, nil
		case "/system/scheduler":
			return []map[string]string{{"name": "mti-failover", "on-event": "SECRET"}, {"name": "other"}}, nil
		case "/system/script/environment":
			return []map[string]string{{"name": "mtiFailBackup", "value": "true"}, {"name": "password", "value": "SECRET"}}, nil
		}
		return nil, fmt.Errorf("unexpected")
	}
	s := buildSnapshot(context.Background(), "rest", read, false)
	if s.Sections["addresses"].Status != "error" || s.Sections["dhcp"].Status != "ok" {
		t.Fatalf("partial result: %+v", s.Sections)
	}
	if len(s.Sections["routes"].Rows) != 3 || len(s.Sections["scheduler"].Rows) != 1 || len(s.Sections["environment"].Rows) != 1 {
		t.Fatalf("wrong filtering: %+v", s.Sections)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "PASSWORD") {
		t.Fatalf("unexpected sensitive fields: %s", b)
	}
	s = buildSnapshot(context.Background(), "api", read, true)
	if s.Sections["addresses"].Status != "error" || s.Sections["dhcp"].Status != "skipped" {
		t.Fatalf("API stream error must stop: %+v", s.Sections)
	}
}
func TestSnapshotRowLimit(t *testing.T) {
	read := func(path string) ([]map[string]string, error) {
		rows := make([]map[string]string, maxSnapshotRows+1)
		for i := range rows {
			rows[i] = map[string]string{"name": "x"}
		}
		return rows, nil
	}
	s := buildSnapshot(context.Background(), "rest", read, false)
	if got := s.Sections["interfaces"]; len(got.Rows) != maxSnapshotRows || !got.Truncated {
		t.Fatalf("limit: %+v", got)
	}
}
func TestCollectNetworkSnapshotREST(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/rest/system/identity":
			fmt.Fprint(w, `[{"name":"router"}]`)
		case "/rest/system/resource":
			fmt.Fprint(w, `[{"version":"7.15", "board-name":"hAP", "architecture-name":"arm"}]`)
		case "/rest/interface":
			fmt.Fprint(w, `[{"name":"ether1","running":"true"}]`)
		case "/rest/ip/address":
			w.WriteHeader(403)
		case "/rest/ip/route":
			fmt.Fprint(w, `[{"dst-address":"0.0.0.0/0","active":"true"}]`)
		case "/rest/ip/dhcp-server":
			w.WriteHeader(403)
		case "/rest/ip/pool":
			fmt.Fprint(w, `[{"name":"default-dhcp","ranges":"192.168.88.2-192.168.88.254","password":"secret"}]`)
		case "/rest/ip/dhcp-server/network":
			fmt.Fprint(w, `[{"address":"192.168.88.0/24","gateway":"192.168.88.1"}]`)
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)
	p := Params{Host: "127.0.0.1", RESTPort: port, User: "admin", Password: "secret", Protocol: "rest"}
	expected := &Info{Protocol: "rest", Identity: "router", RouterOS: "7.15", Architecture: "arm", Diagnostics: &IdentityDiagnostics{ResourceBoard: "hAP"}}
	snap, err := CollectNetworkSnapshot(context.Background(), p, expected)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Sections["addresses"].Status != "error" || snap.Sections["routes"].Status != "ok" || len(snap.Sections["routes"].Rows) != 1 {
		t.Fatalf("%+v", snap)
	}
	if snap.Sections["dhcpServers"].Status != "error" || len(snap.Sections["pools"].Rows) != 1 || len(snap.Sections["dhcpNetworks"].Rows) != 1 {
		t.Fatalf("LAN partial results: %+v", snap.Sections)
	}
	if _, ok := snap.Sections["pools"].Rows[0]["password"]; ok {
		t.Fatal("unexpected sensitive pool field")
	}
	for _, r := range requests {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("write request %s", r)
		}
	}
	before := len(requests)
	expected.Identity = "different"
	if _, err = CollectNetworkSnapshot(context.Background(), p, expected); err == nil {
		t.Fatal("changed identity accepted")
	}
	if len(requests) != before+2 {
		t.Fatalf("data read after mismatch: %v", requests[before:])
	}
	p.Protocol = "auto"
	if _, err = CollectNetworkSnapshot(context.Background(), p, expected); err == nil {
		t.Fatal("auto accepted")
	}
	p.Host = "AA:BB:CC:DD:EE:FF"
	p.Protocol = "mac"
	if _, err = CollectNetworkSnapshot(context.Background(), p, expected); err == nil {
		t.Fatal("MAC accepted")
	}
}

func TestCollectNetworkSnapshotAPI(t *testing.T) {
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
		var received []string
		defer func() { commands <- received }()
		for {
			s, e := readSentence(conn)
			if e != nil || len(s) == 0 {
				return
			}
			received = append(received, s[0])
			switch s[0] {
			case "/login":
				_ = writeSentence(conn, "!done")
			case "/system/identity/print":
				_ = writeSentence(conn, "!re", "=name=router")
				_ = writeSentence(conn, "!done")
			case "/system/resource/print":
				_ = writeSentence(conn, "!re", "=board-name=hAP", "=architecture-name=arm", "=version=7.15")
				_ = writeSentence(conn, "!done")
			case "/ip/route/print":
				_ = writeSentence(conn, "!re", "=dst-address=0.0.0.0/0", "=gateway=192.0.2.1", "=active=true")
				_ = writeSentence(conn, "!done")
			default:
				_ = writeSentence(conn, "!done")
			}
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	p := Params{Host: "127.0.0.1", APIPort: port, User: "admin", Password: "secret", Protocol: "api"}
	expected := &Info{Protocol: "api", Identity: "router", RouterOS: "7.15", Architecture: "arm", Diagnostics: &IdentityDiagnostics{ResourceBoard: "hAP"}}
	s, err := CollectNetworkSnapshot(context.Background(), p, expected)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sections["routes"].Rows) != 1 || s.Sections["routes"].Rows[0]["gateway"] != "192.0.2.1" {
		t.Fatalf("routes: %+v", s.Sections["routes"])
	}
	got := <-commands
	want := []string{"/login", "/system/identity/print", "/system/resource/print", "/interface/print", "/ip/address/print", "/ip/dhcp-client/print", "/ip/route/print", "/system/scheduler/print", "/system/script/environment/print", "/ip/dhcp-server/print", "/ip/pool/print", "/ip/dhcp-server/network/print"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected commands: %v", got)
	}
}

func TestSnapshotLANFieldsAndConfidentiality(t *testing.T) {
	read := func(path string) ([]map[string]string, error) {
		switch path {
		case "/ip/address":
			return []map[string]string{{"address": "192.168.88.1/24", "interface": "bridge", "comment": "mti-lan"}, {"address": "192.0.2.1/24", "comment": "PRIVATE-COMMENT"}, {"address": "198.51.100.1/24"}}, nil
		case "/ip/dhcp-server":
			return []map[string]string{{"name": "defconf", "interface": "bridge", "address-pool": "default-dhcp", "disabled": "false", "secret": "PRIVATE-SECRET"}}, nil
		case "/ip/pool":
			return []map[string]string{{"name": "default-dhcp", "ranges": "192.168.88.10-192.168.88.254", "password": "PRIVATE-SECRET"}}, nil
		case "/ip/dhcp-server/network":
			return []map[string]string{{"address": "192.168.88.0/24", "gateway": "192.168.88.1", "dns-server": "192.168.88.1", "comment": "mti-net"}}, nil
		default:
			return []map[string]string{}, nil
		}
	}
	s := buildSnapshot(context.Background(), "rest", read, false)
	if len(s.Sections["dhcpServers"].Rows) != 1 || len(s.Sections["pools"].Rows) != 1 || len(s.Sections["dhcpNetworks"].Rows) != 1 {
		t.Fatalf("missing LAN data: %+v", s.Sections)
	}
	addr := s.Sections["addresses"].Rows
	if addr[0]["mti-owned"] != "true" || addr[1]["mti-owned"] != "false" {
		t.Fatalf("unexpected ownership: %+v", addr)
	}
	if _, ok := addr[2]["mti-owned"]; ok {
		t.Fatal("missing comment must remain unknown")
	}
	if s.Sections["dhcpNetworks"].Rows[0]["mti-owned"] != "true" {
		t.Fatal("network ownership missing")
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "PRIVATE-") {
		t.Fatalf("private field leaked: %s", b)
	}
}
