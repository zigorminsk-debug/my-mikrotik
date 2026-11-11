package mikrotik

import (
	"strings"
	"testing"
	"time"
)

func TestSecurityGeneration(t *testing.T) {
	dev := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	ifaces := []Iface{{Name: "ether1"}, {Name: "pppoe-out1"}}
	sec := SecuritySettings{Enabled: true, WAN: []string{"ether1", "pppoe-out1"}, Trusted: "192.168.88.0/24,203.0.113.5/32", WinboxPort: "8292", Acknowledged: true}
	r := Generate(dev, ifaces, &Settings{Security: sec}, time.Time{})
	if !r.OK {
		t.Fatal(r.Error)
	}
	for _, v := range []string{"in-interface=ether1", "in-interface=pppoe-out1", "dst-port=8292 src-address=203.0.113.5/32 action=accept", "connection-state=established,related action=accept", "/ipv6 firewall filter add", "protocol=icmpv6 action=accept", "address=192.168.88.0/24,203.0.113.5/32", "/ip service disable [find where name=api]", "/tool mac-server mac-winbox set allowed-interface-list=none", ":error"} {
		if !strings.Contains(r.Script, v) {
			t.Errorf("missing %q", v)
		}
	}
	if strings.Index(r.Script, "mti-sec-0-est4") > strings.Index(r.Script, "mti-sec-0-drop4") {
		t.Fatal("drop was installed before management exceptions")
	}
	if strings.Contains(r.Script, "chain=forward") {
		t.Fatal("forward traffic should not be modified")
	}
}
func TestSecurityValidation(t *testing.T) {
	dev := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	sec := SecuritySettings{Enabled: true, WAN: []string{"ether1"}, Trusted: "192.168.1.0/24", WinboxPort: "8291", Acknowledged: true}
	for _, bad := range []SecuritySettings{
		{Enabled: true, WAN: []string{"ether1"}, Trusted: "0.0.0.0/0", WinboxPort: "8291", Acknowledged: true},
		{Enabled: true, WAN: []string{"missing"}, Trusted: "192.168.1.0/24", WinboxPort: "8291", Acknowledged: true},
		{Enabled: true, WAN: []string{"ether1"}, Trusted: "::/0", WinboxPort: "8291", Acknowledged: true},
		{Enabled: true, WAN: []string{"ether1"}, Trusted: "192.168.1.0/24", WinboxPort: "0", Acknowledged: true},
	} {
		r := Generate(dev, []Iface{{Name: "ether1"}}, &Settings{Security: bad}, time.Time{})
		if r.OK {
			t.Fatalf("expected error for %+v", bad)
		}
	}
	sec.Acknowledged = false
	if r := Generate(dev, []Iface{{Name: "ether1"}}, &Settings{Security: sec}, time.Time{}); r.OK {
		t.Fatal("ack required")
	}
}

func TestSecurityIsAppliedSeparately(t *testing.T) {
	dev := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	s := Settings{Security: SecuritySettings{Enabled: true, WAN: []string{"ether1"}, Trusted: "192.168.88.0/24", WinboxPort: "8291", Acknowledged: true}, LAN: LanSettings{Enabled: true}}
	res := Generate(dev, []Iface{{Name: "ether1"}}, &s, time.Time{})
	if res.OK || !strings.Contains(res.Error, "выключите остальные разделы") {
		t.Fatalf("expected independent script: %+v", res)
	}
}

// Комментарии правил должны начинаться с mti-sec-: по этому префиксу скрипт
// проверяет повторное применение и ищет свои правила при расстановке drop.
func TestSecurityRuleCommentsKeepPrefix(t *testing.T) {
	dev := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	sec := SecuritySettings{Enabled: true, WAN: []string{"ether1"}, Trusted: "192.168.88.0/24", WinboxPort: "8291", Acknowledged: true}
	r := Generate(dev, []Iface{{Name: "ether1", Type: "ether"}}, &Settings{Security: sec}, time.Time{})
	if !r.OK {
		t.Fatal(r.Error)
	}
	if strings.Contains(r.Script, "comment=comment=") {
		t.Fatal("двойной префикс comment=: правила не найдутся по ^mti-sec-")
	}
	for _, want := range []string{
		"action=accept comment=mti-sec-0-dhcp4",
		"action=accept comment=mti-sec-0-est4",
		"action=drop comment=mti-sec-0-drop4",
		"[find where comment=mti-sec-0-est4]",
	} {
		if !strings.Contains(r.Script, want) {
			t.Errorf("missing %q", want)
		}
	}
}
