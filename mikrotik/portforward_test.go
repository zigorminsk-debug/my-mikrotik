package mikrotik

import (
	"strings"
	"testing"
	"time"
)

func TestPortForward(t *testing.T) {
	rule := PortForwardRule{WAN: "ether1", Protocol: "tcp", ExternalPort: "8443", InternalIP: "192.168.88.10", InternalPort: "443", PublicIP: "203.0.113.5", Hairpin: true, LAN: "192.168.88.0/24"}
	settings := Settings{PortForward: PortForwardSettings{Enabled: true, Rules: []PortForwardRule{rule}}}
	result := Generate(nil, nil, &settings, time.Time{})
	if !result.OK {
		t.Fatal(result.Error)
	}
	for _, want := range []string{`action=dst-nat`, attr("to-addresses", "192.168.88.10"), `mti-forward-0-loop`, `connection-nat-state=dstnat action=masquerade`} {
		if !strings.Contains(result.Script, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(result.Script, "/ip firewall filter") {
		t.Fatal("must not modify filter rules")
	}
}

func TestPortForwardValidation(t *testing.T) {
	valid := PortForwardRule{WAN: "ether1", Protocol: "tcp", ExternalPort: "80", InternalIP: "192.168.1.2", InternalPort: "80"}
	for _, change := range []func(*PortForwardRule){
		func(r *PortForwardRule) { r.ExternalPort = "0" },
		func(r *PortForwardRule) { r.InternalPort = "65536" },
		func(r *PortForwardRule) { r.Protocol = "icmp" },
		func(r *PortForwardRule) { r.InternalIP = "bad" },
		func(r *PortForwardRule) { r.Source = "::/0" },
		func(r *PortForwardRule) { r.Hairpin = true },
		func(r *PortForwardRule) { r.WAN = "" },
		func(r *PortForwardRule) {
			r.Hairpin = true
			r.PublicIP = "203.0.113.5"
			r.LAN = "192.168.1.0/24"
			r.Source = "192.168.1.5"
		},
	} {
		r := valid
		change(&r)
		if len(validatePortForward(PortForwardSettings{Rules: []PortForwardRule{r}})) == 0 {
			t.Errorf("accepted invalid rule: %+v", r)
		}
	}
	if len(validatePortForward(PortForwardSettings{Rules: []PortForwardRule{valid, valid}})) == 0 {
		t.Fatal("duplicate accepted")
	}
	if len(validatePortForward(PortForwardSettings{Rules: []PortForwardRule{valid}})) != 0 {
		t.Fatal("valid rejected")
	}
}

func TestPortForwardDisabled(t *testing.T) {
	result := Generate(nil, nil, &Settings{PortForward: PortForwardSettings{Rules: []PortForwardRule{{}}}}, time.Time{})
	if !result.OK || strings.Contains(result.Script, "/ip firewall nat") {
		t.Fatal("disabled section must not alter NAT")
	}
}

func TestQuoteRouterOSVariable(t *testing.T) {
	if got := rosQuote("wan$evil"); got != `"wan\$evil"` {
		t.Fatalf("variable expansion not escaped: %s", got)
	}
}
