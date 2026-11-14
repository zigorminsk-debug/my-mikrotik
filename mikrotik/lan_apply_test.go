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

func lanFixture() (LANApplyRequest, map[string][]map[string]string) {
	d := deviceWith("routeros")
	d.Identity = "router"
	d.Protocol = "rest"
	d.RouterOS = "7.15"
	d.Architecture = "arm"
	d.Diagnostics = &IdentityDiagnostics{ResourceBoard: "hAP"}
	req := LANApplyRequest{Device: d, Interfaces: []Iface{{Name: "bridge", Type: "bridge"}}, LAN: LanSettings{Enabled: true, Interface: "bridge", Address: "192.168.50.1/24"}, DHCP: DHCPSettings{Enabled: true, Interface: "bridge", LeaseTime: "1d"}}
	rows := map[string][]map[string]string{
		"/interface":              {{".id": "*1", "name": "bridge", "disabled": "false"}},
		"/interface/bridge/port":  {{".id": "*1", "interface": "ether2", "bridge": "bridge"}},
		"/ip/address":             {{".id": "*2", "address": "192.168.88.1/24", "network": "192.168.88.0", "interface": "bridge", "dynamic": "false", "disabled": "false", "invalid": "false"}},
		"/ip/dhcp-server":         {{".id": "*3", "name": "defconf", "interface": "bridge", "address-pool": "default-dhcp", "dynamic": "false", "disabled": "false", "invalid": "false"}},
		"/ip/pool":                {{".id": "*4", "name": "default-dhcp", "ranges": "192.168.88.2-192.168.88.254", "next-pool": "none"}},
		"/ip/dhcp-server/network": {{".id": "*5", "address": "192.168.88.0/24", "gateway": "192.168.88.1", "dns-server": "192.168.88.1,1.1.1.1"}},
	}
	return req, rows
}
func readLANFixture(rows map[string][]map[string]string) snapshotReader {
	return func(path string) ([]map[string]string, error) {
		r, ok := rows[path]
		if !ok {
			return nil, fmt.Errorf("unexpected read")
		}
		return r, nil
	}
}
func TestLANPlanChangesDefaultWithoutDeletingOldObjects(t *testing.T) {
	req, rows := lanFixture()
	plan, err := buildLANPlan(req, readLANFixture(rows))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# MTI-LAN-GUARDED-APPLY", "/ip pool add", "/ip dhcp-server network set *5 address=192.168.50.0/24 gateway=192.168.50.1 dns-server=192.168.50.1,1.1.1.1", "/ip dhcp-server set *3", "/ip address set *2 address=192.168.50.1/24 network=192.168.50.0"} {
		if !strings.Contains(plan.Script, want) {
			t.Errorf("missing %s\n%s", want, plan.Script)
		}
	}
	if strings.Contains(plan.Script, " remove ") || strings.Index(plan.Script, "/ip address set") < strings.Index(plan.Script, "/ip dhcp-server set") {
		t.Fatal(plan.Script)
	}
	if len(plan.Fingerprint) != 64 || !strings.Contains(plan.Summary, "отката нет") && !strings.Contains(plan.Summary, "Отката нет") {
		t.Fatal(plan)
	}
	req.DHCP.Enabled = false
	plan, err = buildLANPlan(req, readLANFixture(rows))
	if err != nil || strings.Contains(plan.Script, "/ip dhcp") || !strings.Contains(plan.Summary, "DHCP и DNS не меняются") {
		t.Fatalf("%v %+v", err, plan)
	}
	req, rows = lanFixture()
	rows["/ip/address"] = []map[string]string{}
	rows["/ip/dhcp-server"] = []map[string]string{}
	rows["/ip/dhcp-server/network"] = []map[string]string{}
	plan, err = buildLANPlan(req, readLANFixture(rows))
	if err != nil || !strings.Contains(plan.Script, "/ip address add") || !strings.Contains(plan.Script, "/ip dhcp-server add") {
		t.Fatalf("%v %+v", err, plan)
	}
}
func TestLANPlanRefusesAmbiguityAndConflicts(t *testing.T) {
	cases := []struct {
		name   string
		change func(*LANApplyRequest, map[string][]map[string]string)
	}{
		{"disabled interface", func(_ *LANApplyRequest, r map[string][]map[string]string) { r["/interface"][0]["disabled"] = "true" }},
		{"bridge member", func(_ *LANApplyRequest, r map[string][]map[string]string) {
			r["/interface/bridge/port"] = append(r["/interface/bridge/port"], map[string]string{"interface": "bridge"})
		}},
		{"dynamic", func(_ *LANApplyRequest, r map[string][]map[string]string) { r["/ip/address"][0]["dynamic"] = "true" }},
		{"missing flag", func(_ *LANApplyRequest, r map[string][]map[string]string) { delete(r["/ip/address"][0], "invalid") }},
		{"point to point", func(_ *LANApplyRequest, r map[string][]map[string]string) {
			r["/ip/address"][0]["network"] = "192.168.88.99"
		}},
		{"unknown bridge port", func(_ *LANApplyRequest, r map[string][]map[string]string) {
			r["/interface/bridge/port"] = append(r["/interface/bridge/port"], map[string]string{"bridge": "bridge"})
		}},
		{"unknown address interface", func(_ *LANApplyRequest, r map[string][]map[string]string) { r["/ip/address"][0]["interface"] = "" }},
		{"many addresses", func(_ *LANApplyRequest, r map[string][]map[string]string) {
			r["/ip/address"] = append(r["/ip/address"], r["/ip/address"][0])
		}},
		{"bad id", func(_ *LANApplyRequest, r map[string][]map[string]string) {
			r["/ip/address"][0][".id"] = "*1; :put danger"
		}},
		{"overlap", func(_ *LANApplyRequest, r map[string][]map[string]string) {
			r["/ip/address"] = append(r["/ip/address"], map[string]string{"address": "192.168.50.20/24", "interface": "ether1"})
		}},
		{"other server", func(_ *LANApplyRequest, r map[string][]map[string]string) {
			r["/ip/dhcp-server"] = append(r["/ip/dhcp-server"], map[string]string{"interface": "vlan2"})
		}},
		{"overlapping network", func(_ *LANApplyRequest, r map[string][]map[string]string) {
			r["/ip/dhcp-server/network"] = append(r["/ip/dhcp-server/network"], map[string]string{".id": "*6", "address": "192.168.50.0/24"})
		}},
		{"mismatched interface", func(q *LANApplyRequest, _ map[string][]map[string]string) { q.DHCP.Interface = "ether2" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, r := lanFixture()
			tc.change(&q, r)
			if p, e := buildLANPlan(q, readLANFixture(r)); e == nil {
				t.Fatalf("accepted %+v", p)
			}
		})
	}
}
func TestLANPlanAndApplyREST(t *testing.T) {
	req, rows := lanFixture()
	writes := 0
	var source string
	changed := false
	runFail := false
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/rest")
		switch r.Method + " " + path {
		case "GET /system/identity":
			fmt.Fprint(w, `[{"name":"router"}]`)
		case "GET /system/resource":
			fmt.Fprint(w, `[{"version":"7.15","architecture-name":"arm","board-name":"hAP"}]`)
		case "GET /system/script":
			fmt.Fprint(w, `[]`)
		case "POST /system/script/remove":
			writes++
			fmt.Fprint(w, `{}`)
		case "PUT /system/script":
			writes++
			var v struct {
				Source string `json:"source"`
			}
			_ = json.NewDecoder(r.Body).Decode(&v)
			source = v.Source
			fmt.Fprint(w, `{}`)
		case "POST /system/script/run":
			writes++
			if runFail {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"message":"SECRET"}`)
				return
			}
			fmt.Fprint(w, `{}`)
		default:
			if r.Method != "GET" {
				t.Errorf("unexpected write %s %s", r.Method, path)
				w.WriteHeader(500)
				return
			}
			recs, ok := rows[path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			if changed && path == "/ip/address" {
				recs = []map[string]string{{".id": "*2", "address": "192.168.89.1/24", "interface": "bridge", "dynamic": "false", "disabled": "false", "invalid": "false"}}
			}
			if r.URL.Query().Get(".proplist") != lanProperties[path] {
				t.Error("missing allowlist", path)
			}
			_ = json.NewEncoder(w).Encode(recs)
		}
	}))
	defer router.Close()
	u, _ := url.Parse(router.URL)
	_, ps, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(ps)
	req.Conn = Params{Host: "127.0.0.1", RESTPort: port, User: "admin", Password: "SECRET", Protocol: "rest"}
	plan, err := PlanLAN(context.Background(), req)
	if err != nil || writes != 0 {
		t.Fatalf("plan %v writes %d", err, writes)
	}
	req.Script = plan.Script
	req.Fingerprint = plan.Fingerprint
	if _, err = ApplyLAN(context.Background(), req); err == nil || writes != 0 {
		t.Fatalf("unconfirmed %v %d", err, writes)
	}
	req.Acknowledged = true
	changed = true
	if _, err = ApplyLAN(context.Background(), req); err == nil || writes != 0 {
		t.Fatalf("stale %v %d", err, writes)
	}
	changed = false
	if _, err = ApplyLAN(context.Background(), req); err != nil || writes != 3 || source != plan.Script {
		t.Fatalf("apply %v %d %s", err, writes, source)
	}
	if strings.Contains(source, "SECRET") {
		t.Fatal("credential in script")
	}
	runFail = true
	if _, err = ApplyLAN(context.Background(), req); err == nil || !strings.Contains(err.Error(), "частично") || strings.Contains(err.Error(), "SECRET") || writes != 6 {
		t.Fatalf("failure result %v, writes %d", err, writes)
	}
}
func TestLANPlanGeneralApplyMarkerBlocked(t *testing.T) {
	for _, p := range []string{"api", "rest", "mac"} {
		host := "127.0.0.1"
		if p == "mac" {
			host = "AA:BB:CC:DD:EE:FF"
		}
		if _, err := ApplyScript(context.Background(), Params{Host: host, User: "admin", Protocol: p}, "# MTI-LAN-GUARDED-APPLY\n:put 1"); err == nil {
			t.Fatal(p)
		}
	}
}

func TestLANPlanAndApplyAPI(t *testing.T) {
	req, rows := lanFixture()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	allCommands := make(chan []string, 2)
	go func() {
		for i := 0; i < 2; i++ {
			conn, e := ln.Accept()
			if e != nil {
				return
			}
			var cmds []string
			for {
				sentence, e := readSentence(conn)
				if e != nil || len(sentence) == 0 {
					break
				}
				cmd := sentence[0]
				cmds = append(cmds, cmd)
				switch cmd {
				case "/login":
				case "/system/identity/print":
					_ = writeSentence(conn, "!re", "=name=router")
				case "/system/resource/print":
					_ = writeSentence(conn, "!re", "=version=7.15", "=architecture-name=arm", "=board-name=hAP")
				case "/system/script/print", "/system/script/remove", "/system/script/add", "/system/script/run":
				default:
					path := strings.TrimSuffix(cmd, "/print")
					for _, rec := range rows[path] {
						fields := []string{"!re"}
						for key, val := range rec {
							fields = append(fields, "="+key+"="+val)
						}
						_ = writeSentence(conn, fields...)
					}
				}
				_ = writeSentence(conn, "!done")
			}
			_ = conn.Close()
			allCommands <- cmds
		}
	}()
	req.Device.Protocol = "api"
	req.Conn = Params{Host: "127.0.0.1", APIPort: ln.Addr().(*net.TCPAddr).Port, User: "admin", Protocol: "api"}
	plan, err := PlanLAN(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	cmds := <-allCommands
	for _, c := range cmds {
		if c != "/login" && !strings.HasSuffix(c, "/print") {
			t.Fatalf("unexpected write during plan: %v", cmds)
		}
	}
	req.Acknowledged = true
	req.Fingerprint = plan.Fingerprint
	req.Script = plan.Script
	if _, err = ApplyLAN(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	cmds = <-allCommands
	if strings.Join(cmds, ",") == "" || !strings.Contains(strings.Join(cmds, ","), "/system/script/run") {
		t.Fatal(cmds)
	}
}
