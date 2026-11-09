package mikrotik

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWANGenerate(t *testing.T) {
	s := Settings{WAN: WANSettings{Enabled: true, Links: []WANLink{
		{Interface: "ether1", Mode: "static", Address: "192.0.2.2/24", Gateway: "192.0.2.1", Distance: 1, NAT: true, CheckGateway: true},
		{Interface: "ether2", Mode: "dhcp", Distance: 2, NAT: true},
	}}}
	res := Generate(deviceWith("routeros"), []Iface{{Name: "ether1", Type: "ether"}, {Name: "ether2", Type: "ether"}}, &s, time.Time{})
	if !res.OK {
		t.Fatal(res.Error)
	}
	for _, want := range []string{"check-gateway=ping", "default-route-distance=2", "use-peer-dns=no", attr("gateway", "192.0.2.1%ether1"), "mti-wan-0", "mti-wan-1"} {
		if !strings.Contains(res.Script, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(res.Script, "remove [find dst-address") {
		t.Fatal("must preserve unmanaged routes")
	}
}

func TestWANValidation(t *testing.T) {
	good := WANLink{Interface: "ether1", Mode: "static", Address: "192.0.2.2/24", Gateway: "192.0.2.1", Distance: 1}
	for _, change := range []func(*WANLink){
		func(r *WANLink) { r.Gateway = "198.51.100.1" },
		func(r *WANLink) { r.Gateway = "192.0.2.2" },
		func(r *WANLink) { r.Address = "::1/64" },
		func(r *WANLink) { r.Distance = 0 },
		func(r *WANLink) { r.Interface = "" },
		func(r *WANLink) { r.Mode = "unknown" },
		func(r *WANLink) { r.Mode = "dhcp"; r.CheckGateway = true },
	} {
		r := good
		change(&r)
		if len(validateWAN(WANSettings{Links: []WANLink{r}}, PPPoESettings{})) == 0 {
			t.Errorf("accepted %+v", r)
		}
	}
	if len(validateWAN(WANSettings{Links: []WANLink{good, good}}, PPPoESettings{})) == 0 {
		t.Fatal("duplicate links accepted")
	}
	if len(validateWAN(WANSettings{Links: []WANLink{good}}, PPPoESettings{Enabled: true, DefaultRoute: true})) == 0 {
		t.Fatal("PPPoE route conflict accepted")
	}
	if len(validateWAN(WANSettings{Links: []WANLink{good}}, PPPoESettings{})) != 0 {
		t.Fatal("valid rejected")
	}
}

func TestWANMissingPackages(t *testing.T) {
	res := Generate(deviceWith("system"), nil, &Settings{WAN: WANSettings{Enabled: true}}, time.Time{})
	if !res.OK || len(res.Skipped) == 0 {
		t.Fatal("missing DHCP must skip WAN")
	}
	if strings.Contains(res.Script, "/ip dhcp-client add") {
		t.Fatal("unavailable WAN emitted")
	}
}

func TestWANLocalConflict(t *testing.T) {
	lan := LanSettings{Enabled: true, Interface: "bridge", Address: "192.168.88.1/24"}
	for _, r := range []WANLink{
		{Interface: "bridge", Mode: "dhcp"},
		{Interface: "ether1", Mode: "static", Address: "192.168.88.2/25"},
	} {
		if len(validateWANLocal(WANSettings{Links: []WANLink{r}}, lan)) == 0 {
			t.Fatalf("conflict accepted: %+v", r)
		}
	}
}

func TestWANNetworkAndBroadcast(t *testing.T) {
	for _, address := range []string{"192.0.2.0/24", "192.0.2.255/24", "192.0.2.2/32"} {
		if len(validateWAN(WANSettings{Links: []WANLink{{Interface: "ether1", Mode: "static", Address: address, Gateway: "192.0.2.1", Distance: 1}}}, PPPoESettings{})) == 0 {
			t.Errorf("accepted %s", address)
		}
	}
}

func TestLTEPrimaryOrBackup(t *testing.T) {
	for _, distance := range []int{1, 2} {
		s := Settings{WAN: WANSettings{Enabled: true, Links: []WANLink{{Interface: "lte1", Mode: "lte", APN: "internet", Distance: distance, NAT: true}}}}
		res := Generate(deviceWith("routeros"), []Iface{{Name: "lte1", Type: "lte"}}, &s, time.Time{})
		if !res.OK {
			t.Fatal(res.Error)
		}
		for _, want := range []string{"/interface lte apn set", "apn=internet", fmt.Sprintf("default-route-distance=%d", distance), "apn-profiles=mti-wan-0-apn", "out-interface=lte1"} {
			if !strings.Contains(res.Script, want) {
				t.Errorf("missing %s", want)
			}
		}
		if strings.Contains(res.Script, "/ip dhcp-client add") {
			t.Fatal("LTE must not create Ethernet DHCP client")
		}
	}
}

func TestLTEInterfaceValidation(t *testing.T) {
	s := WANSettings{Links: []WANLink{{Mode: "lte", Interface: "ether1", Distance: 1, APN: "internet"}}}
	if len(validateWANInterfaces(s, []Iface{{Name: "ether1", Type: "ether"}})) == 0 {
		t.Fatal("Ethernet accepted as LTE")
	}
	s.Links[0].Interface = "lte1"
	if len(validateWANInterfaces(s, nil)) == 0 {
		t.Fatal("missing LTE accepted")
	}
	s.Links[0].APN = ""
	if len(validateWAN(s, PPPoESettings{})) == 0 {
		t.Fatal("empty APN accepted")
	}
}
