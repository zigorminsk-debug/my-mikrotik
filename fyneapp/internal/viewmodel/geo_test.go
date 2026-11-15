package viewmodel

import (
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
