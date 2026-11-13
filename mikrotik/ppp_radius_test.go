package mikrotik

import (
	"context"
	"strings"
	"testing"
	"time"
)

func validRadius() PPPRadiusSettings {
	return PPPRadiusSettings{Enabled: true, Address: "192.0.2.10", AuthenticationPort: "1812", AccountingPort: "1813", Secret: "RandomSecret_1234567890", Acknowledged: true}
}
func radiusDevice() *Info { return &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}} }

func TestPPPRadiusPreview(t *testing.T) {
	r := validRadius()
	res := Generate(radiusDevice(), nil, &Settings{PPPRadius: r}, time.Time{})
	if !res.OK {
		t.Fatalf("%s", res.Error)
	}
	for _, s := range []string{"# MTI-PPP-RADIUS-GUARDED-APPLY", ":if ([:len [/radius find]] > 0)", "/radius add service=ppp address=192.0.2.10 authentication-port=1812 accounting-port=1813 secret=RandomSecret_1234567890 comment=mti-ppp-radius disabled=no", "/ppp aaa set use-radius=yes", "PPPoE", "скрипте", "исполняемый"} {
		if !strings.Contains(res.Script, s) {
			t.Errorf("missing %q: %s", s, res.Script)
		}
	}
	for _, s := range []string{"/radius remove", "/interface l2tp-server", "/interface pptp-server", "/ip route", "/system scheduler"} {
		if strings.Contains(res.Script, s) {
			t.Errorf("unexpected %s", s)
		}
	}
	for _, protocol := range []string{"api", "rest", "auto", "mac"} {
		host := "127.0.0.1"
		if protocol == "mac" {
			host = "AA:BB:CC:DD:EE:FF"
		}
		_, err := ApplyScript(context.Background(), Params{Host: host, User: "admin", Protocol: protocol}, res.Script)
		if err == nil || !strings.Contains(err.Error(), "общий endpoint") {
			t.Fatalf("%s: %v", protocol, err)
		}
	}
}

func TestPPPRadiusValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*PPPRadiusSettings, *Settings)
	}{
		{"no-ack", func(r *PPPRadiusSettings, _ *Settings) { r.Acknowledged = false }},
		{"hostname", func(r *PPPRadiusSettings, _ *Settings) { r.Address = "radius.local" }},
		{"ipv6", func(r *PPPRadiusSettings, _ *Settings) { r.Address = "::1" }},
		{"loopback", func(r *PPPRadiusSettings, _ *Settings) { r.Address = "127.0.0.1" }},
		{"multicast", func(r *PPPRadiusSettings, _ *Settings) { r.Address = "224.0.0.1" }},
		{"invalid-port", func(r *PPPRadiusSettings, _ *Settings) { r.AuthenticationPort = "65536" }},
		{"invalid-acct", func(r *PPPRadiusSettings, _ *Settings) { r.AccountingPort = "0" }},
		{"short-secret", func(r *PPPRadiusSettings, _ *Settings) { r.Secret = "short" }},
		{"secret-injection", func(r *PPPRadiusSettings, _ *Settings) { r.Secret = "aaaaaaaaaaaaaaaa\"\n:put SECRET" }},
		{"mix-wan", func(_ *PPPRadiusSettings, s *Settings) { s.WAN.Enabled = true }},
		{"mix-vpn", func(_ *PPPRadiusSettings, s *Settings) { s.PPTP.Enabled = true }},
		{"mix-geonat", func(_ *PPPRadiusSettings, s *Settings) { s.GeoNAT.Enabled = true }},
		{"mix-pppoe", func(_ *PPPRadiusSettings, s *Settings) { s.PPPoE.Enabled = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validRadius()
			s := &Settings{}
			tc.change(&r, s)
			s.PPPRadius = r
			res := Generate(radiusDevice(), nil, s, time.Time{})
			if res.OK {
				t.Fatalf("unexpected success %s", res.Script)
			}
			if strings.Contains(res.Error, "SECRET") {
				t.Fatal("secret leaked in validation error")
			}
		})
	}
}

func TestPPPRadiusAvailabilityAndIsolation(t *testing.T) {
	for _, dev := range []*Info{{RouterOS: "6.49", Packages: []Package{{Name: "ppp"}}}, radiusDevice()} {
		r := Generate(dev, nil, &Settings{PPPRadius: validRadius()}, time.Time{})
		if !r.OK || !strings.Contains(r.Script, "MTI-PPP-RADIUS-GUARDED-APPLY") {
			t.Fatalf("%+v", r)
		}
	}
	r := Generate(&Info{RouterOS: "6.49", Packages: []Package{{Name: "system"}}}, nil, &Settings{PPPRadius: validRadius()}, time.Time{})
	if !r.OK || strings.Contains(r.Script, "MTI-PPP-RADIUS") || len(r.Skipped) != 1 || r.Skipped[0].ID != "pppRadius" {
		t.Fatalf("%+v", r)
	}
	// Disabled settings are inert, even if fields contain stale values.
	s := validRadius()
	s.Enabled = false
	r = Generate(radiusDevice(), nil, &Settings{PPPRadius: s}, time.Time{})
	if strings.Contains(r.Script, s.Secret) || strings.Contains(r.Script, "/radius add") {
		t.Fatal("disabled RADIUS settings leaked into script")
	}
}
