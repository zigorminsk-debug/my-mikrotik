package viewmodel

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

	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
)

func TestGeoPlanEndToEndREST(t *testing.T) {
	for _, mode := range []string{"success", "preflight", "lost"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var writes int
			var sent string
			router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					mu.Lock()
					writes++
					mu.Unlock()
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /rest/system/identity":
					fmt.Fprint(w, `[{"name":"router"}]`)
				case "GET /rest/system/resource":
					fmt.Fprint(w, `[{"version":"7.25.1","architecture-name":"arm","board-name":"hAP"}]`)
				case "GET /rest/interface":
					fmt.Fprint(w, `[{"name":"ether1","disabled":"false"}]`)
				case "GET /rest/ip/address":
					fmt.Fprint(w, `[{"address":"192.168.88.1/24"}]`)
				case "GET /rest/ip/firewall/filter":
					fmt.Fprint(w, `[]`)
				case "GET /rest/ip/firewall/nat":
					if mode == "preflight" {
						fmt.Fprint(w, `[]`)
					} else {
						fmt.Fprint(w, `[{"chain":"dstnat","action":"dst-nat","disabled":false,"protocol":"tcp","to-addresses":"192.168.88.50","to-ports":"3389","in-interface":"ether1"}]`)
					}
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
					sent = body.Source
					mu.Unlock()
					fmt.Fprint(w, `{}`)
				case "POST /rest/system/script/run":
					if mode == "lost" {
						w.WriteHeader(500)
					}
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer router.Close()
			u, _ := url.Parse(router.URL)
			host, portstr, _ := net.SplitHostPort(u.Host)
			port, _ := strconv.Atoi(portstr)
			device := &mikrotik.Info{Protocol: "rest", Identity: "router", RouterOS: "7.25.1", Architecture: "arm", Diagnostics: &mikrotik.IdentityDiagnostics{ResourceBoard: "hAP"}, Packages: []mikrotik.Package{{Name: "routeros"}}}
			geo := mikrotik.GeoNATSettings{Enabled: true, Resource: "by", WAN: "ether1", Destination: "192.168.88.50", Port: "3389", Protocol: "tcp", Countries: []string{"by"}, Acknowledged: true}
			for i := 0; i < 106; i++ {
				geo.CountryPrefixes = append(geo.CountryPrefixes, fmt.Sprintf("198.18.%d.0/24", i))
			}
			plan, err := BuildGeoPlan(device, []mikrotik.Iface{{Name: "ether1"}}, geo, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			req := plan.Request
			req.Conn = mikrotik.Params{Host: host, RESTPort: port, User: "admin", Password: "secret", Protocol: "rest"}
			_, err = mikrotik.ApplyGeoNAT(context.Background(), req)
			mu.Lock()
			defer mu.Unlock()
			out := GeoApplyOutcome(err, nil)
			switch mode {
			case "success":
				if err != nil || writes != 3 || sent != req.Script {
					t.Fatalf("%v writes=%d", err, writes)
				}
			case "preflight":
				if err == nil || writes != 0 || !strings.Contains(out, "до записи") {
					t.Fatalf("%v %s writes=%d", err, out, writes)
				}
			case "lost":
				if err == nil || writes != 3 || !strings.Contains(out, "частично") || sent != req.Script {
					t.Fatalf("%v %s writes=%d", err, out, writes)
				}
			}
		})
	}
}
