package mikrotik

import (
	"context"
	"strings"
	"testing"
	"time"
)

func usbSettings() WANSettings {
	return WANSettings{Enabled: true, Failover: true, ProbeHost: "8.8.8.8", Links: []WANLink{
		{Mode: "usb-tether", Interface: "lte1", Distance: 1, NAT: true},
		{Mode: "dhcp", Interface: "ether1", Distance: 2, NAT: true},
	}}
}

func TestUSBFailoverValidation(t *testing.T) {
	good := usbSettings()
	if e := validateWAN(good, PPPoESettings{}); len(e) != 0 {
		t.Fatalf("valid USB: %v", e)
	}
	for _, change := range []func(*WANSettings){
		func(s *WANSettings) { s.Failover = false },
		func(s *WANSettings) { s.Links = s.Links[:1] },
		func(s *WANSettings) { s.Links[1].Mode = "static" },
		func(s *WANSettings) { s.Links[0], s.Links[1] = s.Links[1], s.Links[0] },
		func(s *WANSettings) { s.Links[0].CheckGateway = true },
		func(s *WANSettings) { s.Links[1].Interface = s.Links[0].Interface },
	} {
		s := usbSettings()
		change(&s)
		if len(validateWAN(s, PPPoESettings{})) == 0 {
			t.Fatalf("invalid USB accepted: %+v", s)
		}
	}
	if e := validateWANInterfaces(good, []Iface{{Name: "lte1", Type: "ether"}, {Name: "ether1", Type: "ether"}}); len(e) == 0 {
		t.Fatal("non-LTE interface accepted")
	}
}

func TestUSBPreviewGeneration(t *testing.T) {
	d := deviceWith("routeros")
	d.RouterOS = "7.9.2"
	s := Settings{WAN: usbSettings()}
	res := Generate(d, []Iface{{Name: "lte1", Type: "lte"}, {Name: "ether1", Type: "ether"}}, &s, time.Time{})
	if !res.OK {
		t.Fatal(res.Error)
	}
	for _, want := range []string{
		"# MTI-USB-PREVIEW-ONLY", ":local c [/ip dhcp-client find where interface=lte1]", ":local c [/ip dhcp-client find where interface=ether1]",
		`[:len \$c] = 1`, `[/ip dhcp-client get \$c status] = \"bound\"`,
		`gateway=\$primaryGw routing-table=mti-probe`,
		`:if (\$mtiUsbBackup) do={ :set selected \$backupGw }`,
		`gateway=\$selected distance=1`, `routing-table=main`,
		`([/ip route get \$r distance] <= 1)`, `:if ((\$selected = \"\") || \$conflict)`,
		`\$now - \$mtiUsbBad) >= 30s`, `\$now - \$mtiUsbGood) >= 1m`,
		"/ip firewall connection remove [find]", "interval=5s", "mti-usb: backup=",
	} {
		if !strings.Contains(res.Script, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"/interface lte apn", "/interface lte set", "172.26.48.63", "10.254.100.254", "/ip dhcp-client release"} {
		if strings.Contains(res.Script, bad) {
			t.Errorf("unexpected %q", bad)
		}
	}
	if len(res.Script) > maxScriptSize {
		t.Fatal("script exceeds apply size limit")
	}
}

func TestUSBPreviewCannotApply(t *testing.T) {
	_, err := ApplyScript(context.Background(), Params{Host: "127.0.0.1", User: "test", Protocol: "api"}, "# MTI-USB-PREVIEW-ONLY\n:put 1")
	if err == nil || !strings.Contains(err.Error(), "только для предпросмотра") {
		t.Fatalf("unexpected error: %v", err)
	}
}
