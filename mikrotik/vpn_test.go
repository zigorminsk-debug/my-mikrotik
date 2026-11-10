package mikrotik

import (
	"strings"
	"testing"
	"time"
)

func TestVPNGeneration(t *testing.T) {
	dev := &Info{RouterOS: "7.15.3", Packages: []Package{{Name: "routeros"}}}
	key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	wg := WireGuardSettings{Enabled: true, PrivateKey: key, PublicKey: key, Address: "10.10.10.1/24", AllowedIPs: "10.10.10.2/32,192.168.2.0/24", ListenPort: "13231"}
	client := wg
	client.Step = "peer" // клиент настраивается вторым проходом: добавляем peer
	client.Address = "10.10.10.2/24"
	client.Endpoint = "vpn.example.com"
	client.EndpointPort = "13231"
	s := &Settings{WGServer: wg, WGClient: client, OpenVPN: VPNClientSettings{Enabled: true, Server: "vpn.example.com", Port: "1194", User: "alice", Password: "p$word"}, PPTP: VPNClientSettings{Enabled: true, Server: "vpn.example.com", User: "bob", Password: "secret"}}
	res := Generate(dev, nil, s, time.Time{})
	if !res.OK {
		t.Fatal(res.Error)
	}
	for _, want := range []string{"/interface wireguard add name=mti-wg-server", "interface=mti-wg-client", "endpoint-address=vpn.example.com", "allowed-address=10.10.10.2/32,192.168.2.0/24", "/interface ovpn-client add", "verify-server-certificate=yes", `/interface pptp-client add`, `password="p\$word"`} {
		if !strings.Contains(res.Script, want) {
			t.Errorf("missing %q in %s", want, res.Script)
		}
	}
}
func TestVPNValidationAndAvailability(t *testing.T) {
	dev := &Info{RouterOS: "7.1", Packages: []Package{{Name: "routeros"}}}
	res := Generate(dev, nil, &Settings{WGClient: WireGuardSettings{Enabled: true}}, time.Time{})
	if res.OK || !strings.Contains(res.Error, "WireGuard") {
		t.Fatalf("expected validation error: %+v", res)
	}
	old := &Info{RouterOS: "6.49", Packages: []Package{{Name: "routeros"}}}
	res = Generate(old, nil, &Settings{WGServer: WireGuardSettings{Enabled: true}}, time.Time{})
	if !res.OK || len(res.Skipped) != 1 || strings.Contains(res.Script, "/interface wireguard add") {
		t.Fatalf("expected skipped WG: %+v", res)
	}
	res = Generate(&Info{RouterOS: "6.49", Packages: []Package{{Name: "system"}}}, nil, &Settings{PPTP: VPNClientSettings{Enabled: true}}, time.Time{})
	if !res.OK || len(res.Skipped) != 1 {
		t.Fatalf("expected skipped PPP: %+v", res)
	}
}

func TestOpenVPNCertificates(t *testing.T) {
	dev := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	s := &Settings{OVPNCertificates: OVPNCertificateSettings{Enabled: true, ServerName: "vpn.example.com", ClientName: "client1", ExportPassword: "long-secret-password"}}
	result := Generate(dev, nil, s, time.Time{})
	if !result.OK {
		t.Fatal(result.Error)
	}
	for _, want := range []string{"/certificate add name=mti-ovpn-ca", "/certificate sign mti-ovpn-ca", "subject-alt-name=DNS:vpn.example.com", "/certificate sign mti-ovpn-server ca=mti-ovpn-ca", "/certificate sign mti-ovpn-client ca=mti-ovpn-ca", "type=pkcs12 export-passphrase=long-secret-password", ":error"} {
		if !strings.Contains(result.Script, want) {
			t.Errorf("missing %q", want)
		}
	}
	s.OVPNCertificates.ServerName = "192.0.2.10"
	result = Generate(dev, nil, s, time.Time{})
	if !strings.Contains(result.Script, "subject-alt-name=IP:192.0.2.10") {
		t.Fatal(result.Script)
	}
	s.OVPNCertificates.ExportPassword = "short"
	if result = Generate(dev, nil, s, time.Time{}); result.OK {
		t.Fatal("expected validation error")
	}
	old := &Info{RouterOS: "6.49", Packages: []Package{{Name: "routeros"}}}
	if result = Generate(old, nil, s, time.Time{}); !result.OK || len(result.Skipped) != 1 {
		t.Fatalf("expected unavailable: %+v", result)
	}
}

func TestVPNTransportAndPorts(t *testing.T) {
	dev := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	v := VPNClientSettings{Enabled: true, Server: "vpn.example.com", Port: "443", Protocol: "tcp", User: "user", Password: "secret"}
	for _, protocol := range []string{"tcp", "udp", ""} {
		v.Protocol = protocol
		result := Generate(dev, nil, &Settings{OpenVPN: v}, time.Time{})
		if !result.OK {
			t.Fatalf("%s: %s", protocol, result.Error)
		}
		expected := protocol
		if expected == "" {
			expected = "tcp"
		}
		if !strings.Contains(result.Script, "port=443 protocol="+expected) {
			t.Fatalf("unexpected script: %s", result.Script)
		}
	}
	v.Protocol = "invalid"
	if r := Generate(dev, nil, &Settings{OpenVPN: v}, time.Time{}); r.OK {
		t.Fatal("expected invalid protocol error")
	}
	v.Protocol = "udp"
	for _, port := range []string{"0", "65536", "+1", "-1", "abc", ""} {
		v.Port = port
		if r := Generate(dev, nil, &Settings{OpenVPN: v}, time.Time{}); r.OK {
			t.Fatalf("expected invalid port %q", port)
		}
	}
	key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	server := WireGuardSettings{Enabled: true, PrivateKey: key, PublicKey: key, Address: "10.10.10.1/24", AllowedIPs: "10.10.10.2/32", ListenPort: "51820"}
	client := server
	client.Step = "peer"
	client.Address = "10.10.10.2/24"
	client.ListenPort = "51821"
	client.Endpoint = "vpn.example.com"
	client.EndpointPort = "51820"
	result := Generate(dev, nil, &Settings{WGServer: server, WGClient: client}, time.Time{})
	if !result.OK {
		t.Fatal(result.Error)
	}
	for _, want := range []string{"name=mti-wg-server listen-port=51820", "interface=mti-wg-client", "endpoint-port=51820"} {
		if !strings.Contains(result.Script, want) {
			t.Errorf("missing %q", want)
		}
	}
	client.EndpointPort = "65536"
	if r := Generate(dev, nil, &Settings{WGClient: client}, time.Time{}); r.OK {
		t.Fatal("expected invalid WireGuard endpoint port")
	}
	server.ListenPort = "0"
	if r := Generate(dev, nil, &Settings{WGServer: server}, time.Time{}); r.OK {
		t.Fatal("expected invalid WireGuard listen port")
	}
}

func TestOpenVPNExitRouting(t *testing.T) {
	dev := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	s := &Settings{OpenVPN: VPNClientSettings{Enabled: true, Server: "vpn.example.com", Port: "1194", Protocol: "udp", User: "alice", Password: "secret"}}
	for _, tc := range []struct {
		vpn  bool
		want string
	}{{false, "add-default-route=no"}, {true, "add-default-route=yes"}} {
		s.OpenVPN.ExitViaVPN = tc.vpn
		res := Generate(dev, nil, s, time.Time{})
		if !res.OK || !strings.Contains(res.Script, tc.want) || !strings.Contains(res.Script, "route-nopull=yes") {
			t.Fatalf("unexpected result: %+v", res)
		}
	}
	old := &Info{RouterOS: "7.6.1", Packages: []Package{{Name: "routeros"}}}
	res := Generate(old, nil, s, time.Time{})
	if !res.OK || len(res.Skipped) != 1 {
		t.Fatalf("expected unsupported version: %+v", res)
	}
}

func TestWireGuardTwoPass(t *testing.T) {
	dev := &Info{RouterOS: "7.15.3", Packages: []Package{{Name: "routeros"}}}
	key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	// Шаг 1: интерфейс поднимается без приватного ключа — его создаёт роутер.
	step1 := WireGuardSettings{Enabled: true, Step: "interface", Address: "10.10.10.1/24", ListenPort: "13231"}
	res := Generate(dev, nil, &Settings{WGServer: step1}, time.Time{})
	if !res.OK {
		t.Fatal(res.Error)
	}
	if strings.Contains(res.Script, "private-key") {
		t.Errorf("приватный ключ не должен передаваться: %s", res.Script)
	}
	for _, want := range []string{
		"/interface wireguard add name=mti-wg-server listen-port=13231",
		"/ip address add address=10.10.10.1/24 interface=mti-wg-server",
		"mti-wg pubkey=",
		"public-key",
	} {
		if !strings.Contains(res.Script, want) {
			t.Errorf("шаг 1: нет %q в %s", want, res.Script)
		}
	}

	// Шаг 2: peer добавляется к существующему интерфейсу.
	step2 := WireGuardSettings{Enabled: true, Step: "peer", PublicKey: key, AllowedIPs: "10.10.10.2/32"}
	res = Generate(dev, nil, &Settings{WGServer: step2}, time.Time{})
	if !res.OK {
		t.Fatal(res.Error)
	}
	if strings.Contains(res.Script, "/interface wireguard add") {
		t.Errorf("шаг 2 не должен создавать интерфейс заново: %s", res.Script)
	}
	for _, want := range []string{
		"/interface wireguard peers add",
		"interface=mti-wg-server",
		`public-key="` + key + `"`,
		"allowed-address=10.10.10.2/32",
	} {
		if !strings.Contains(res.Script, want) {
			t.Errorf("шаг 2: нет %q в %s", want, res.Script)
		}
	}

	// Шаг 2 не требует адрес интерфейса и порт, но требует корректный ключ.
	if r := Generate(dev, nil, &Settings{WGServer: WireGuardSettings{Enabled: true, Step: "peer", PublicKey: "нет", AllowedIPs: "10.10.10.0/24"}}, time.Time{}); r.OK {
		t.Fatal("ожидалась ошибка некорректного публичного ключа")
	}
	if r := Generate(dev, nil, &Settings{WGServer: WireGuardSettings{Enabled: true, Step: "peer", PublicKey: key, AllowedIPs: "10.10.10.0/24"}}, time.Time{}); !r.OK {
		t.Fatalf("шаг 2 не должен требовать адрес интерфейса: %s", r.Error)
	}
	// Клиент на шаге 2 требует адрес и порт сервера.
	if r := Generate(dev, nil, &Settings{WGClient: WireGuardSettings{Enabled: true, Step: "peer", PublicKey: key, AllowedIPs: "10.10.10.0/24"}}, time.Time{}); r.OK {
		t.Fatal("ожидалась ошибка: у клиента не указан сервер")
	}
}
