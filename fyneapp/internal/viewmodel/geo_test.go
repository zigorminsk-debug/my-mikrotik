package viewmodel

import (
	"context"
	"fmt"
	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCountryCodes(t *testing.T) {
	v, e := CountryCodes("BY, pl;DE")
	if e != nil || strings.Join(v, ",") != "by,de,pl" {
		t.Fatal(v, e)
	}
	for _, s := range []string{"by by", "../x", "яz", "abc", "aa bb cc dd ee ff gg hh ii jj kk ll mm"} {
		if _, e := CountryCodes(s); e == nil {
			t.Fatal(s)
		}
	}
}
func TestGeoPreviewGuarded(t *testing.T) {
	device := &mikrotik.Info{RouterOS: "7.25.1", Packages: []mikrotik.Package{{Name: "routeros"}}}
	ifaces := []mikrotik.Iface{{Name: "ether1"}}
	g := mikrotik.GeoNATSettings{Enabled: true, Resource: "by", WAN: "ether1", Destination: "192.168.88.50", Port: "3389", Protocol: "tcp", Manual: "192.0.2.1", Acknowledged: true}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, e := GeoPreview(device, ifaces, g, at)
	if e != nil || !strings.Contains(s, "# MTI-GEONAT-GUARDED-APPLY") || !strings.Contains(s, "192.168.88.50") {
		t.Fatal(s, e)
	}
	g.Acknowledged = false
	if _, e = GeoPreview(device, ifaces, g, at); e == nil {
		t.Fatal("ack ignored")
	}
	g.Acknowledged = true
	g.Manual = "0.0.0.0/0"
	if _, e = GeoPreview(device, ifaces, g, at); e == nil {
		t.Fatal("invalid prefix accepted")
	}
	g.Manual = "192.0.2.1"
	if _, e = GeoPreview(&mikrotik.Info{RouterOS: "7.1"}, ifaces, g, at); e == nil {
		t.Fatal("skipped feature accepted")
	}
}
func TestGeoPreviewClipsLongOutput(t *testing.T) {
	d := &mikrotik.Info{RouterOS: "7.1", Packages: []mikrotik.Package{{Name: "routeros"}}}
	g := mikrotik.GeoNATSettings{Enabled: true, Resource: "test", WAN: "ether1", Destination: "192.168.88.50", Port: "3389", Protocol: "tcp", Countries: []string{"by"}, Acknowledged: true}
	for i := 0; i < 250; i++ {
		g.CountryPrefixes = append(g.CountryPrefixes, fmtIP(i))
	}
	s, e := GeoPreview(d, []mikrotik.Iface{{Name: "ether1"}}, g, time.Now())
	if e != nil || !strings.Contains(s, "Предпросмотр сокращён") {
		t.Fatal(len(s), e)
	}
}
func fmtIP(i int) string { return "192.0.2." + strconv.Itoa(i) + "/32" }

func TestGeoPlanRetainsExactDetachedInputs(t *testing.T) {
	d := &mikrotik.Info{Protocol: "rest", Identity: "router", RouterOS: "7.25.1", Architecture: "arm", Diagnostics: &mikrotik.IdentityDiagnostics{ResourceBoard: "hAP"}, Packages: []mikrotik.Package{{Name: "routeros"}}}
	i := []mikrotik.Iface{{Name: "ether1"}}
	g := mikrotik.GeoNATSettings{Enabled: true, Resource: "by", WAN: "ether1", Destination: "192.168.88.50", Port: "3389", Protocol: "tcp", Countries: []string{"by"}, CountryPrefixes: []string{"192.0.2.0/24"}, Acknowledged: true}
	plan, err := BuildGeoPlan(d, i, g, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d.Identity = "other"
	d.Packages[0].Name = "other"
	d.Diagnostics.ResourceBoard = "other"
	i[0].Name = "bad"
	g.CountryPrefixes[0] = "0.0.0.0/0"
	g.Countries[0] = "zz"
	req := plan.Request
	if req.Device.Identity != "router" || req.Device.Diagnostics.ResourceBoard != "hAP" || req.Interfaces[0].Name != "ether1" || req.Geo.Countries[0] != "by" || req.Geo.CountryPrefixes[0] != "192.0.2.0/24" {
		t.Fatal("preview not detached")
	}
	regenerated := mikrotik.Generate(req.Device, req.Interfaces, &mikrotik.Settings{GeoNAT: req.Geo}, req.GeneratedAt)
	if regenerated.Script != req.Script {
		t.Fatal("script/plan mismatch")
	}
}
func TestGeoApplyOutcome(t *testing.T) {
	if !strings.Contains(GeoApplyOutcome(&mikrotik.GeoPreflightError{Cause: fmt.Errorf("denied")}, context.DeadlineExceeded), "до записи") {
		t.Fatal("preflight lost")
	}
	for _, v := range []string{GeoApplyOutcome(fmt.Errorf("lost"), nil), GeoApplyOutcome(nil, context.DeadlineExceeded)} {
		if !strings.Contains(v, "частично") || !strings.Contains(v, "не повторяйте") {
			t.Fatal(v)
		}
	}
	if !strings.Contains(GeoApplyOutcome(nil, nil), "не подтверждает защиту") {
		t.Fatal("false success claim")
	}
}
