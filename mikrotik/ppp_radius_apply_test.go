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

func TestPPPRadiusPreflight(t *testing.T) {
	cases := []struct {
		name           string
		l2tp, aaa, rad []map[string]string
		fail           string
		ok             bool
	}{
		{"required", []map[string]string{{"enabled": "true", "use-ipsec": "required"}}, []map[string]string{{"use-radius": "false"}}, nil, "", true},
		{"yes", []map[string]string{{"enabled": "yes", "use-ipsec": "yes"}}, []map[string]string{{"use-radius": "yes"}}, nil, "", true},
		{"disabled", []map[string]string{{"enabled": "false", "use-ipsec": "required"}}, nil, nil, "", false},
		{"unknown", []map[string]string{{"use-ipsec": "required"}}, nil, nil, "", false},
		{"no-ipsec", []map[string]string{{"enabled": "true", "use-ipsec": "no"}}, nil, nil, "", false},
		{"aaa-unknown", []map[string]string{{"enabled": "true", "use-ipsec": "required"}}, []map[string]string{{}}, nil, "", false},
		{"radius-exists", []map[string]string{{"enabled": "true", "use-ipsec": "required"}}, []map[string]string{{"use-radius": "false"}}, []map[string]string{{".id": "*1"}}, "", false},
		{"radius-unreadable", []map[string]string{{"enabled": "true", "use-ipsec": "required"}}, []map[string]string{{"use-radius": "false"}}, nil, "/radius", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			read := func(path string) ([]map[string]string, error) {
				if path == tc.fail {
					return nil, fmt.Errorf("SECRET")
				}
				switch path {
				case "/interface/l2tp-server/server":
					return tc.l2tp, nil
				case "/ppp/aaa":
					return tc.aaa, nil
				case "/radius":
					return tc.rad, nil
				}
				return nil, fmt.Errorf("unknown")
			}
			err := checkPPPRadiusPreflight(read)
			if (err == nil) != tc.ok {
				t.Fatalf("%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatal("error leak")
			}
		})
	}
}

func radiusApplyFixture(port int) PPPRadiusApplyRequest {
	device := &Info{Protocol: "rest", Identity: "router", RouterOS: "7.15", Architecture: "arm", Diagnostics: &IdentityDiagnostics{ResourceBoard: "hAP"}, Packages: []Package{{Name: "routeros"}}}
	r := validRadius()
	when := time.Date(2026, 11, 13, 12, 0, 0, 123, time.UTC)
	result := Generate(device, nil, &Settings{PPPRadius: r}, when)
	return PPPRadiusApplyRequest{Conn: Params{Host: "127.0.0.1", RESTPort: port, User: "admin", Password: "router-password", Protocol: "rest"}, Device: device, Radius: r, GeneratedAt: when, Script: result.Script}
}

func TestPPPRadiusRESTApply(t *testing.T) {
	for _, tc := range []struct {
		name, l2tp, aaa, radius string
		runFail                 bool
		want                    string
		writes                  int
	}{
		{"success", `{"enabled":true,"use-ipsec":"required","ipsec-secret":"SECRET-IPSEC"}`, `{"use-radius":false}`, `[]`, false, "", 3},
		{"radius-null", `{"enabled":true,"use-ipsec":"required"}`, `{"use-radius":false}`, `null`, false, "не удалось", 0},
		{"radius-conflict", `{"enabled":true,"use-ipsec":"yes"}`, `{"use-radius":false}`, `[{".id":"*1","secret":"SECRET-RADIUS"}]`, false, "уже есть", 0},
		{"missing-l2tp", `{"enabled":false,"use-ipsec":"required"}`, `{"use-radius":false}`, `[]`, false, "выключен", 0},
		{"ipsec-disabled", `{"enabled":true,"use-ipsec":"no"}`, `{"use-radius":false}`, `[]`, false, "IPsec", 0},
		{"run-failure", `{"enabled":true,"use-ipsec":"required"}`, `{"use-radius":false}`, `[]`, true, "частично", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			var source string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls = append(calls, r.Method+" "+r.URL.Path)
				mu.Unlock()
				switch r.Method + " " + r.URL.Path {
				case "GET /rest/system/identity":
					fmt.Fprint(w, `{"name":"router"}`)
				case "GET /rest/system/resource":
					fmt.Fprint(w, `{"version":"7.15","board-name":"hAP","architecture-name":"arm"}`)
				case "GET /rest/interface/l2tp-server/server":
					if strings.Contains(r.URL.Query().Get(".proplist"), "secret") {
						t.Error("secret requested")
					}
					fmt.Fprint(w, tc.l2tp)
				case "GET /rest/ppp/aaa":
					fmt.Fprint(w, tc.aaa)
				case "GET /rest/radius":
					if r.URL.Query().Get(".proplist") != ".id" {
						t.Error("unsafe radius properties")
					}
					fmt.Fprint(w, tc.radius)
				case "GET /rest/system/script":
					fmt.Fprint(w, `[]`)
				case "POST /rest/system/script/remove":
					fmt.Fprint(w, `{}`)
				case "PUT /rest/system/script":
					var body struct {
						Source string `json:"source"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					mu.Lock()
					source = body.Source
					mu.Unlock()
					fmt.Fprint(w, `{}`)
				case "POST /rest/system/script/run":
					if tc.runFail {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"SECRET-RADIUS"}`)
					} else {
						fmt.Fprint(w, `{}`)
					}
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			_, ps, _ := net.SplitHostPort(u.Host)
			port, _ := strconv.Atoi(ps)
			req := radiusApplyFixture(port)
			proto, err := ApplyPPPRadius(context.Background(), req)
			if tc.want == "" {
				if err != nil || proto != "rest" {
					t.Fatalf("%s %v", proto, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("wanted %s: %v", tc.want, err)
			}
			if err != nil && (strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), req.Radius.Secret)) {
				t.Fatal("secret leaked in error")
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
				t.Fatalf("writes %d calls %v", writes, calls)
			}
			if writes > 0 && source != req.Script {
				t.Fatal("wrong source")
			}
		})
	}
}

func TestPPPRadiusRefusesChangedPreview(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PPPRadiusApplyRequest)
		want   string
	}{
		{"source", func(r *PPPRadiusApplyRequest) { r.Script += "\n:put 1" }, "предпросмотр"},
		{"settings", func(r *PPPRadiusApplyRequest) { r.Radius.Address = "192.0.2.11" }, "предпросмотр"},
		{"noack", func(r *PPPRadiusApplyRequest) { r.Radius.Acknowledged = false }, "подтвердите"},
		{"no-date", func(r *PPPRadiusApplyRequest) { r.GeneratedAt = time.Time{} }, "подтвердите"},
		{"mac", func(r *PPPRadiusApplyRequest) {
			r.Conn.Protocol = "mac"
			r.Conn.Host = "AA:BB:CC:DD:EE:FF"
			r.Device.Protocol = "mac"
		}, "API/REST"},
		{"injected-model", func(r *PPPRadiusApplyRequest) {
			r.Device.Model = "router\n:put 1"
			r.Script = Generate(r.Device, nil, &Settings{PPPRadius: r.Radius}, r.GeneratedAt).Script
		}, "некорректные"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := radiusApplyFixture(1)
			tc.mutate(&r)
			_, err := ApplyPPPRadius(context.Background(), r)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
	for _, marker := range []string{"# MTI-PPP-RADIUS-PREVIEW-ONLY", "# MTI-PPP-RADIUS-GUARDED-APPLY"} {
		_, err := ApplyScript(context.Background(), Params{Host: "127.0.0.1", User: "admin", Protocol: "api"}, marker)
		if err == nil || !strings.Contains(err.Error(), "общий endpoint") {
			t.Fatal(err)
		}
	}
}

func TestPPPRadiusAPISameSession(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan []string, 1)
	go func() {
		conn, e := ln.Accept()
		if e != nil {
			done <- nil
			return
		}
		defer conn.Close()
		var cmds []string
		defer func() { done <- cmds }()
		for {
			words, e := readSentence(conn)
			if e != nil || len(words) == 0 {
				return
			}
			cmds = append(cmds, words[0])
			switch words[0] {
			case "/system/identity/print":
				_ = writeSentence(conn, "!re", "=name=router")
			case "/system/resource/print":
				_ = writeSentence(conn, "!re", "=version=7.15", "=board-name=hAP", "=architecture-name=arm")
			case "/interface/l2tp-server/server/print":
				_ = writeSentence(conn, "!re", "=enabled=true", "=use-ipsec=required")
			case "/ppp/aaa/print":
				_ = writeSentence(conn, "!re", "=use-radius=false")
			}
			_ = writeSentence(conn, "!done")
		}
	}()
	req := radiusApplyFixture(0)
	req.Conn.Protocol = "api"
	req.Conn.APIPort = ln.Addr().(*net.TCPAddr).Port
	req.Device.Protocol = "api"
	req.Script = Generate(req.Device, nil, &Settings{PPPRadius: req.Radius}, req.GeneratedAt).Script
	proto, err := ApplyPPPRadius(context.Background(), req)
	if err != nil || proto != "api" {
		t.Fatalf("%s %v", proto, err)
	}
	cmds := <-done
	want := []string{"/login", "/system/identity/print", "/system/resource/print", "/interface/l2tp-server/server/print", "/ppp/aaa/print", "/radius/print", "/system/script/print", "/system/script/remove", "/system/script/add", "/system/script/run"}
	if strings.Join(cmds, ",") != strings.Join(want, ",") {
		t.Fatalf("%v", cmds)
	}
}
