package mikrotik

import (
	"context"
	"strings"
	"testing"
)

func TestLANPreviewAndDHCPInterfaceValidation(t *testing.T) {
	d := deviceWith("routeros")
	ifs := []Iface{{Name: "bridge", Type: "bridge"}, {Name: "ether2", Type: "ether"}}
	for _, dhcp := range []bool{false, true} {
		s := Settings{LAN: LanSettings{Enabled: true, Interface: "bridge", Address: "192.168.88.1/24"}, DHCP: DHCPSettings{Enabled: dhcp, Interface: "bridge", LeaseTime: "1d"}}
		r := Generate(d, ifs, &s, testTime())
		if !r.OK || !strings.Contains(r.Script, "# MTI-LAN-PREVIEW-ONLY") {
			t.Fatalf("LAN script not protected: %+v", r)
		}
	}
	s := Settings{LAN: LanSettings{Enabled: true, Interface: "bridge", Address: "192.168.88.1/24"}, DHCP: DHCPSettings{Enabled: true, Interface: "ether2", LeaseTime: "1d"}}
	if r := Generate(d, ifs, &s, testTime()); r.OK || !strings.Contains(r.Error, "совпадать") {
		t.Fatalf("mismatched LAN DHCP accepted: %+v", r)
	}
	s = Settings{DNS: DNSSettings{Enabled: true, Servers: "1.1.1.1"}}
	if r := Generate(d, ifs, &s, testTime()); !r.OK || strings.Contains(r.Script, "# MTI-LAN-PREVIEW-ONLY") {
		t.Fatalf("DNS-only affected: %+v", r)
	}
}

func TestLANMarkerRejectsAllTransports(t *testing.T) {
	for _, protocol := range []string{"api", "rest", "auto", "mac"} {
		host := "127.0.0.1"
		if protocol == "mac" {
			host = "AA:BB:CC:DD:EE:FF"
		}
		_, err := ApplyScript(context.Background(), Params{Host: host, User: "test", Protocol: protocol}, "# MTI-LAN-PREVIEW-ONLY\n:put 1")
		if err == nil || !strings.Contains(err.Error(), "только для предпросмотра") {
			t.Errorf("%s: %v", protocol, err)
		}
	}
}
