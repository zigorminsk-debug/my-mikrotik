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

func TestTerminalCommandError(t *testing.T) {
	for _, s := range []string{
		"failure: can not run on slave interface", "syntax error (line 1 column 6)",
		"bad command name bridge", "not enough permissions", "input does not match any value of interface",
	} {
		if err := terminalCommandError(s); err == nil || !strings.Contains(err.Error(), s) {
			t.Errorf("terminal error not reported: %q: %v", s, err)
		}
	}
	if err := terminalCommandError("name=bridge;type=bridge\nname=ether1;type=ether"); err != nil {
		t.Fatalf("valid output rejected: %v", err)
	}
}

func TestIdentityDiagnosticsREST(t *testing.T) {
	for _, tc := range []struct {
		name, resource, rb               string
		status                           int
		wantModel, wantBoard, wantStatus string
	}{
		{"distinct names", `{"board-name":"hAP ac3","version":"7.15","architecture-name":"arm"}`, `[{"model":"RBD53iG","board":"legacy","serial-number":"SECRET-SERIAL"}]`, 200, "hAP ac3", "RBD53iG", "ok"},
		{"model fallback", `{"version":"7.15"}`, `[{"model":"RBMODEL","board":"legacy"}]`, 200, "RBMODEL", "RBMODEL", "ok"},
		{"board fallback", `{"version":"7.15"}`, `[{"board":"legacy"}]`, 200, "legacy", "legacy", "ok"},
		{"empty", `{"board-name":"CHR","version":"7.15"}`, `[]`, 200, "CHR", "", "empty"},
		{"unavailable", `{"board-name":"CHR","version":"7.15"}`, `{"message":"not available"}`, 403, "CHR", "", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var methods []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method+" "+r.URL.Path)
				switch r.URL.Path {
				case "/rest/system/identity":
					fmt.Fprint(w, `[{"name":"private identity"}]`)
				case "/rest/system/resource":
					fmt.Fprintf(w, "[%s]", tc.resource)
				case "/rest/system/routerboard":
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.rb)
				case "/rest/system/package":
					fmt.Fprint(w, `[]`)
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			_, portStr, _ := net.SplitHostPort(u.Host)
			port, _ := strconv.Atoi(portStr)
			info, err := Collect(context.Background(), Params{Host: "127.0.0.1", RESTPort: port, User: "admin", Password: "SECRET-PASSWORD", Protocol: "rest"})
			if err != nil {
				t.Fatal(err)
			}
			if info.Model != tc.wantModel || info.Board != tc.wantBoard || info.Diagnostics.RouterboardStatus != tc.wantStatus {
				t.Fatalf("unexpected info: %+v / %+v", info, info.Diagnostics)
			}
			for _, method := range methods {
				if !strings.HasPrefix(method, "GET ") {
					t.Errorf("unexpected non-read request: %s", method)
				}
			}
			b, _ := json.Marshal(info.Diagnostics)
			if strings.Contains(string(b), "SECRET") {
				t.Fatalf("sensitive field in diagnostics: %s", b)
			}
		})
	}
}
