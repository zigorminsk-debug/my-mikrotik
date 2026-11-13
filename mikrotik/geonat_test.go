package mikrotik

import (
	"strings"
	"testing"
	"time"
)

func TestGeoNATScript(t *testing.T) {
	dev := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	s := GeoNATSettings{Enabled: true, Resource: "server1", WAN: "pppoe-out1", Destination: "192.168.88.10", Port: "8443", Protocol: "tcp", Countries: []string{"by"}, CountryPrefixes: []string{"5.44.44.0/24", "5.44.44.3/24"}, Manual: "203.0.113.10\n198.51.100.0/24", Acknowledged: true}
	r := Generate(dev, []Iface{{Name: "pppoe-out1"}}, &Settings{GeoNAT: s}, time.Unix(100, 0))
	if !r.OK {
		t.Fatal(r.Error)
	}
	for _, want := range []string{"address=5.44.44.0/24", "address=203.0.113.10/32", "chain=forward in-interface=pppoe-out1 connection-nat-state=dstnat dst-address=192.168.88.10 protocol=tcp dst-port=8443", "src-address-list=!mti-nat-", "action=accept", "action=drop", "/ip firewall address-list remove [find where comment~", "this NAT whitelist script was already applied"} {
		if !strings.Contains(r.Script, want) {
			t.Errorf("missing %q", want)
		}
	}
	if got := strings.Count(r.Script, "address=5.44.44.0/24"); got != 1 {
		t.Fatalf("duplicate subnet: %d", got)
	}
	if strings.Contains(r.Script, "/ip firewall nat") {
		t.Fatal("must not modify NAT rules")
	}
	if strings.Index(r.Script, "/ip firewall address-list add") > strings.Index(r.Script, "/ip firewall filter add") {
		t.Fatal("whitelist must be populated before firewall rules")
	}
}
func TestGeoNATValidation(t *testing.T) {
	dev := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	good := GeoNATSettings{Enabled: true, Resource: "test", WAN: "ether1", Destination: "192.168.88.10", Port: "80", Protocol: "tcp", Manual: "203.0.113.4", Acknowledged: true}
	invalid := []GeoNATSettings{good, good, good, good, good, good, good}
	invalid[0].Manual = "0.0.0.0/0"
	invalid[1].WAN = "missing"
	invalid[2].Resource = "bad;cmd"
	invalid[3].Manual = "::ffff:192.0.2.1/120"
	invalid[4].Port = "0"
	invalid[5].Countries = []string{"by"}
	invalid[6].Acknowledged = false
	for _, bad := range invalid {
		r := Generate(dev, []Iface{{Name: "ether1"}}, &Settings{GeoNAT: bad}, time.Now())
		if r.OK {
			t.Fatalf("expected error: %+v", bad)
		}
	}
	r := Generate(dev, []Iface{{Name: "ether1"}}, &Settings{GeoNAT: good, LAN: LanSettings{Enabled: true}}, time.Now())
	if r.OK || !strings.Contains(r.Error, "отдельным скриптом") {
		t.Fatalf("expected isolation: %+v", r)
	}
}
func TestGeoNATResourceIsolation(t *testing.T) {
	dev := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	s := GeoNATSettings{Enabled: true, Resource: "foo", WAN: "ether1", Destination: "10.0.0.1", Port: "80", Protocol: "tcp", Manual: "1.2.3.4", Acknowledged: true}
	a := Generate(dev, []Iface{{Name: "ether1"}}, &Settings{GeoNAT: s}, time.Unix(1, 0))
	s.Resource = "foo-bar"
	b := Generate(dev, []Iface{{Name: "ether1"}}, &Settings{GeoNAT: s}, time.Unix(1, 0))
	marker := strings.Split(strings.Split(a.Script, "list=mti-nat-")[1], " ")[0]
	if strings.Contains(b.Script, "list=mti-nat-"+marker) {
		t.Fatal("resource markers collide")
	}
}

func TestGeoNATImportOnlyAndIsolation(t *testing.T) {
	dev := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	geo := GeoNATSettings{Enabled: true, Resource: "test", WAN: "ether1", Destination: "192.168.88.10", Port: "80", Protocol: "tcp", Manual: "203.0.113.4", Acknowledged: true}
	ifaces := []Iface{{Name: "ether1", Type: "ether"}}
	r := Generate(dev, ifaces, &Settings{GeoNAT: geo}, time.Now())
	if !r.OK || !strings.Contains(r.Script, "# MTI-GEONAT-IMPORT-ONLY") {
		t.Fatalf("missing protection: %+v", r)
	}
	for _, extra := range []Settings{
		{GeoNAT: geo, WAN: WANSettings{Enabled: true, Links: []WANLink{{Mode: "dhcp", Interface: "ether1", Distance: 1}}}},
		{GeoNAT: geo, PortForward: PortForwardSettings{Enabled: true}},
	} {
		r = Generate(dev, ifaces, &extra, time.Now())
		if r.OK || !strings.Contains(r.Error, "отдельным скриптом") {
			t.Fatalf("mixed section accepted: %+v", r)
		}
	}
}
