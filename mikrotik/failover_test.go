package mikrotik

import (
	"strings"
	"testing"
	"time"
)

func TestFailoverGeneration(t *testing.T) {
	for _, mode := range []string{"dhcp", "static", "lte"} {
		iface, kind := "ether1", "ether"
		if mode == "lte" {
			iface, kind = "lte1", "lte"
		}
		s := Settings{WAN: WANSettings{Enabled: true, Failover: true, ProbeHost: "8.8.4.4", Links: []WANLink{
			{Mode: mode, Interface: iface, Address: "192.0.2.2/24", Gateway: "192.0.2.1", Distance: 1},
			{Mode: "dhcp", Interface: "ether2", Distance: 2},
		}}}
		device := deviceWith("routeros")
		device.RouterOS = "7.22.3"
		res := Generate(device, []Iface{{Name: iface, Type: kind}, {Name: "ether2", Type: "ether"}}, &s, time.Time{})
		if !res.OK {
			t.Fatal(res.Error)
		}
		for _, want := range []string{"routing-table=mti-probe", "blackhole", "chain=output protocol=icmp dst-address=8.8.4.4", "new-routing-mark=mti-probe", `\$now - \$mtiFailBad >= 30s`, `\$now - \$mtiFailGood >= 1m`, "interval=5s", "default-route-distance="} {
			if !strings.Contains(res.Script, want) {
				t.Errorf("%s missing %s", mode, want)
			}
		}
	}
}

func TestFailoverValidation(t *testing.T) {
	s := WANSettings{Failover: true, ProbeHost: "bad", Links: []WANLink{{Mode: "dhcp", Interface: "ether1", Distance: 1}}}
	if len(validateWAN(s, PPPoESettings{})) < 2 {
		t.Fatal("single provider and invalid host accepted")
	}
}
