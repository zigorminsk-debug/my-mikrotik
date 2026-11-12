package mikrotik

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Записывающий мок-сервер RouterOS API (интерфейсы + выполнение скрипта)
// ---------------------------------------------------------------------------

type mockRec struct {
	mu   sync.Mutex
	cmds []string
}

func (r *mockRec) add(s string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmds = append(r.cmds, s)
}

func (r *mockRec) contains(sub string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// startScriptMock — мок RouterOS API: вход, интерфейсы, операции со скриптами.
func startScriptMock(t *testing.T) (port int, rec *mockRec, stop func()) {
	t.Helper()
	return startScriptMockWithRunError(t, "")
}

func startScriptMockWithRunError(t *testing.T, runError string) (port int, rec *mockRec, stop func()) {
	t.Helper()
	return startScriptMockWithErrors(t, runError, "")
}

func startScriptMockWithErrors(t *testing.T, runError, bridgeError string) (port int, rec *mockRec, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rec = &mockRec{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		for {
			s, err := readSentence(conn)
			if err != nil || len(s) == 0 {
				return
			}
			rec.add(strings.Join(s, " | "))
			switch s[0] {
			case "/login":
				a := attrs(s[1:])
				if a["name"] == mockUser && a["password"] == mockPassword {
					_ = writeSentence(conn, "!done")
				} else {
					_ = writeSentence(conn, "!trap", "=message=invalid user name or password")
					_ = writeSentence(conn, "!done")
				}
			case "/interface/print":
				_ = writeSentence(conn, "!re", "=name=ether1", "=type=ethernet", "=comment=WAN", "=disabled=false")
				_ = writeSentence(conn, "!re", "=name=ether2", "=type=ethernet", "=disabled=false")
				_ = writeSentence(conn, "!re", "=name=wlan1", "=type=wireless", "=disabled=true")
				_ = writeSentence(conn, "!re", "=name=bridge", "=type=bridge", "=disabled=false")
				_ = writeSentence(conn, "!done")
			case "/interface/bridge/port/print":
				if bridgeError != "" {
					_ = writeSentence(conn, "!trap", "=message="+bridgeError)
				} else {
					_ = writeSentence(conn, "!re", "=interface=ether2", "=bridge=bridge")
				}
				_ = writeSentence(conn, "!done")
			case "/system/script/remove":
				// Роутер отвечает trap, если скрипта нет — клиент должен это игнорировать.
				_ = writeSentence(conn, "!trap", "=message=no such item")
				_ = writeSentence(conn, "!done")
			case "/system/script/add":
				_ = writeSentence(conn, "!done")
			case "/system/script/run":
				if runError != "" {
					_ = writeSentence(conn, "!trap", "=message="+runError)
					_ = writeSentence(conn, "!done")
					continue
				}
				a := attrs(s[1:])
				if a["number"] == applyScriptName || a["numbers"] == applyScriptName {
					_ = writeSentence(conn, "!done")
				} else {
					_ = writeSentence(conn, "!trap", "=message=expected number")
					_ = writeSentence(conn, "!done")
				}
			default:
				_ = writeSentence(conn, "!trap", "=message=unexpected command "+s[0])
				_ = writeSentence(conn, "!done")
			}
		}
	}()
	stop = func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	return ln.Addr().(*net.TCPAddr).Port, rec, stop
}

// startScriptRESTMock — мок REST API: интерфейсы и создание/запуск скрипта.
func startScriptRESTMock(t *testing.T) (port int, rec *mockRec, stop func()) {
	t.Helper()
	rec = &mockRec{}
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			u, p, ok := r.BasicAuth()
			if !ok || u != mockUser || p != mockPassword {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"message":"HTTP 401 Unauthorized"}`)
				return
			}
			h(w, r)
		}
	}
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/interface", auth(func(w http.ResponseWriter, r *http.Request) {
		rec.add("GET /rest/interface")
		write(w, http.StatusOK, []map[string]any{
			{"name": "ether1", "type": "ethernet", "comment": "WAN", "disabled": false},
			{"name": "ether2", "type": "ethernet", "disabled": false},
			{"name": "wifi1", "type": "wifi", "disabled": false},
		})
	}))
	mux.HandleFunc("/rest/interface/bridge/port", auth(func(w http.ResponseWriter, r *http.Request) {
		rec.add("GET /rest/interface/bridge/port")
		write(w, http.StatusOK, []map[string]any{{"interface": "ether2", "bridge": "bridge"}})
	}))
	mux.HandleFunc("/rest/system/script/remove", auth(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.add("POST /rest/system/script/remove " + string(body))
		write(w, http.StatusBadRequest, map[string]string{"message": "no such item"})
	}))
	mux.HandleFunc("/rest/system/script/run", auth(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.add("POST /rest/system/script/run " + string(body))
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if num, _ := m["number"].(string); num == applyScriptName {
			write(w, http.StatusOK, []any{})
			return
		}
		write(w, http.StatusBadRequest, map[string]string{"message": "expected number"})
	}))
	mux.HandleFunc("/rest/system/script", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			write(w, http.StatusMethodNotAllowed, map[string]string{"message": "PUT only"})
			return
		}
		body, _ := io.ReadAll(r.Body)
		rec.add("PUT /rest/system/script " + string(body))
		write(w, http.StatusOK, []map[string]string{{".id": "*1"}})
	}))
	srv := httptest.NewServer(mux)
	u, _ := url.Parse(srv.URL)
	p, _ := strconv.Atoi(strings.Split(u.Host, ":")[1])
	return p, rec, srv.Close
}

// ---------------------------------------------------------------------------
// Вычисления по подсети
// ---------------------------------------------------------------------------

func TestDeriveLAN(t *testing.T) {
	cases := []struct {
		name        string
		cidr        string
		wantNetwork string
		wantGateway string
		wantPool    string
		wantErr     bool
	}{
		{"типовой /24, роутер .1", "192.168.88.1/24", "192.168.88.0/24", "192.168.88.1", "192.168.88.2-192.168.88.254", false},
		{"роутер в середине", "192.168.88.100/24", "192.168.88.0/24", "192.168.88.100", "192.168.88.1-192.168.88.99,192.168.88.101-192.168.88.254", false},
		{"малая /30", "10.0.0.1/30", "10.0.0.0/30", "10.0.0.1", "10.0.0.2", false},
		{"крошечная /32 — пустой пул, без ошибки", "10.1.1.1/32", "10.1.1.1/32", "10.1.1.1", "", false},
		{"IPv6 не поддерживается", "fe80::1/64", "", "", "", true},
		{"мусор", "abc", "", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			network, gateway, pool, err := deriveLAN(c.cidr)
			if c.wantErr {
				if err == nil {
					t.Fatal("ожидалась ошибка")
				}
				return
			}
			if err != nil {
				t.Fatalf("deriveLAN: %v", err)
			}
			if network != c.wantNetwork || gateway != c.wantGateway || pool != c.wantPool {
				t.Errorf("got (%q, %q, %q), want (%q, %q, %q)",
					network, gateway, pool, c.wantNetwork, c.wantGateway, c.wantPool)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Доступность разделов по пакетам
// ---------------------------------------------------------------------------

func deviceWith(pkgs ...string) *Info {
	info := &Info{Model: "hEX S", RouterOS: "7.15.3 (stable)", Packages: []Package{}}
	for _, p := range pkgs {
		info.Packages = append(info.Packages, Package{Name: p, Version: "7.15.3"})
	}
	return info
}

func featureByID(t *testing.T, feats []FeatureInfo, id string) FeatureInfo {
	t.Helper()
	for _, f := range feats {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("раздел %q не найден", id)
	return FeatureInfo{}
}

func TestAnalyzeFeatures_MissingDhcp(t *testing.T) {
	feats := AnalyzeFeatures(deviceWith("system", "ppp"), []Iface{{Name: "ether1", Type: "ethernet"}})
	dhcp := featureByID(t, feats, featDHCP)
	if dhcp.Available {
		t.Error("DHCP должен быть недоступен без пакета dhcp")
	}
	if !strings.Contains(dhcp.Reason, "необходимый пакет отсутствует") || !strings.Contains(dhcp.Reason, "dhcp") {
		t.Errorf("причина: %q", dhcp.Reason)
	}
	pppoe := featureByID(t, feats, featPPPoE)
	if !pppoe.Available {
		t.Error("PPPoE должен быть доступен при пакете ppp")
	}
}

func TestAnalyzeFeatures_Wifi(t *testing.T) {
	t.Run("нет пакета", func(t *testing.T) {
		feats := AnalyzeFeatures(deviceWith("system", "dhcp", "ppp"), []Iface{{Name: "ether1", Type: "ethernet"}})
		w := featureByID(t, feats, featWiFi)
		if w.Available {
			t.Fatal("Wi-Fi должен быть недоступен")
		}
		if !strings.Contains(w.Reason, "необходимый пакет отсутствует") {
			t.Errorf("причина: %q", w.Reason)
		}
	})
	t.Run("пакет есть, интерфейсов нет", func(t *testing.T) {
		feats := AnalyzeFeatures(deviceWith("system", "wireless"), []Iface{{Name: "ether1", Type: "ethernet"}})
		w := featureByID(t, feats, featWiFi)
		if w.Available {
			t.Fatal("Wi-Fi должен быть недоступен без интерфейсов")
		}
		if !strings.Contains(w.Reason, "нет беспроводных интерфейсов") {
			t.Errorf("причина: %q", w.Reason)
		}
	})
	t.Run("legacy wireless", func(t *testing.T) {
		feats := AnalyzeFeatures(deviceWith("system", "wireless"),
			[]Iface{{Name: "wlan1", Type: "wireless"}})
		w := featureByID(t, feats, featWiFi)
		if !w.Available || w.Mode != "legacy" {
			t.Errorf("available=%v mode=%q", w.Available, w.Mode)
		}
	})
	t.Run("wifiwave2", func(t *testing.T) {
		feats := AnalyzeFeatures(deviceWith("system", "wifiwave2"),
			[]Iface{{Name: "wifi1", Type: "wifi"}})
		w := featureByID(t, feats, featWiFi)
		if !w.Available || w.Mode != "wave2" {
			t.Errorf("available=%v mode=%q", w.Available, w.Mode)
		}
	})
}

// ---------------------------------------------------------------------------
// Генерация скрипта
// ---------------------------------------------------------------------------

func testTime() time.Time { return time.Date(2026, 11, 8, 21, 15, 0, 0, time.UTC) }

func fullSettings() Settings {
	return Settings{
		LAN:   LanSettings{Enabled: true, Interface: "ether2", Address: "192.168.88.1/24"},
		DHCP:  DHCPSettings{Enabled: true, Interface: "ether2", LeaseTime: "1d"},
		DNS:   DNSSettings{Enabled: true, Servers: "8.8.8.8,8.8.4.4", AllowRemote: true},
		NTP:   NTPSettings{Enabled: true, Servers: "pool.ntp.org", TimeZone: "Europe/Minsk"},
		PPPoE: PPPoESettings{Enabled: true, Interface: "ether1", Name: "pppoe-out1", User: "ispuser", Password: "secret123", DefaultRoute: true, UsePeerDNS: true, NAT: true},
		WiFi:  WiFiSettings{Enabled: true, Interface: "wlan1", SSID: "My Home", Passphrase: "passw0rd123", Country: "BY"},
	}
}

func TestGenerate_LegacyWiFi(t *testing.T) {
	device := deviceWith("system", "dhcp", "ppp", "wireless")
	ifaces := []Iface{
		{Name: "ether1", Type: "ethernet", Comment: "WAN"},
		{Name: "ether2", Type: "ethernet"},
		{Name: "wlan1", Type: "wireless"},
	}
	s := fullSettings()
	res := Generate(device, ifaces, &s, testTime())
	if !res.OK {
		t.Fatalf("ошибки валидации: %s", res.Error)
	}
	script := res.Script
	wantLines := []string{
		"для hEX S",
		"RouterOS 7.15.3 (stable)",
		"/ip address add address=192.168.88.1/24 interface=ether2 comment=mti-lan disabled=no",
		"/ip pool add name=mti-pool ranges=192.168.88.2-192.168.88.254",
		"/ip dhcp-server add name=mti-dhcp interface=ether2 address-pool=mti-pool lease-time=1d disabled=no",
		"/ip dhcp-server network add address=192.168.88.0/24 gateway=192.168.88.1 dns-server=8.8.8.8,8.8.4.4 comment=mti-net",
		"/ip dns set servers=8.8.8.8,8.8.4.4 allow-remote-requests=yes",
		"/system ntp client servers add address=pool.ntp.org",
		"/system clock set time-zone-name=Europe/Minsk",
		"/interface pppoe-client add name=pppoe-out1 interface=ether1 user=ispuser password=secret123 add-default-route=yes use-peer-dns=yes dial-on-demand=no disabled=no",
		"/ip firewall nat add chain=srcnat out-interface=pppoe-out1 action=masquerade comment=mti-pppoe",
		`/interface wireless security-profiles set [find where name="mti-wifi"] mode=dynamic-keys authentication-types=wpa2-psk unicast-ciphers=aes-ccm group-ciphers=aes-ccm wpa2-pre-shared-key=passw0rd123`,
		`/interface wireless set wlan1 disabled=no mode=ap-bridge ssid="My Home" country=belarus security-profile=mti-wifi`,
	}
	for _, w := range wantLines {
		if !strings.Contains(script, w) {
			t.Errorf("в скрипте нет строки:\n  %s\n--- скрипт ---\n%s", w, script)
		}
	}
}

func TestGenerate_Wave2WiFi(t *testing.T) {
	device := deviceWith("system", "dhcp", "wifiwave2")
	ifaces := []Iface{{Name: "wifi1", Type: "wifi"}, {Name: "ether1", Type: "ethernet"}}
	s := fullSettings()
	s.PPPoE.Enabled = false
	s.WiFi.Interface = "wifi1"
	res := Generate(device, ifaces, &s, testTime())
	if !res.OK {
		t.Fatalf("ошибки валидации: %s", res.Error)
	}
	for _, w := range []string{
		"/interface wifi configuration add name=mti-wifi mode=ap security.authentication-types=wpa2-psk ssid=\"My Home\" security.passphrase=passw0rd123 country=belarus",
		"/interface wifi set wifi1 configuration=mti-wifi disabled=no",
	} {
		if !strings.Contains(res.Script, w) {
			t.Errorf("нет строки %q:\n%s", w, res.Script)
		}
	}
	if strings.Contains(res.Script, "/interface wireless set") {
		t.Error("для wave2 не должно быть legacy-команд")
	}
}

func TestGenerate_SkipsUnavailableDhcp(t *testing.T) {
	device := deviceWith("system", "ppp", "wireless") // dhcp-пакета нет
	ifaces := []Iface{{Name: "ether1", Type: "ethernet"}, {Name: "wlan1", Type: "wireless"}}
	s := fullSettings()
	res := Generate(device, ifaces, &s, testTime())
	if !res.OK {
		t.Fatalf("недоступный раздел не должен ломать генерацию: %s", res.Error)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].ID != featDHCP {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
	if !strings.Contains(res.Skipped[0].Reason, "необходимый пакет отсутствует") {
		t.Errorf("reason: %q", res.Skipped[0].Reason)
	}
	if strings.Contains(res.Script, "dhcp-server add") || strings.Contains(res.Script, "/ip pool add") {
		t.Error("скрипт не должен содержать DHCP-команды")
	}
	// Остальные разделы на месте.
	if !strings.Contains(res.Script, "/ip address add") || !strings.Contains(res.Script, "/ip dns set") {
		t.Error("остальные разделы должны присутствовать")
	}
}

func TestGenerate_ValidationErrors(t *testing.T) {
	device := deviceWith("system", "dhcp", "ppp", "wireless")
	ifaces := []Iface{{Name: "ether1", Type: "ethernet"}, {Name: "ether2", Type: "ethernet"}, {Name: "wlan1", Type: "wireless"}}

	t.Run("адрес без маски", func(t *testing.T) {
		s := fullSettings()
		s.LAN.Address = "192.168.88.1"
		res := Generate(device, ifaces, &s, testTime())
		if res.OK || !strings.Contains(res.Error, "Адреса") || !strings.Contains(res.Error, "маски") {
			t.Errorf("ok=%v err=%q", res.OK, res.Error)
		}
	})
	t.Run("PPPoE без логина", func(t *testing.T) {
		s := fullSettings()
		s.PPPoE.User = ""
		res := Generate(device, ifaces, &s, testTime())
		if res.OK || !strings.Contains(res.Error, "PPPoE: укажите логин") {
			t.Errorf("ok=%v err=%q", res.OK, res.Error)
		}
	})
	t.Run("короткий Wi-Fi пароль", func(t *testing.T) {
		s := fullSettings()
		s.WiFi.Passphrase = "123"
		res := Generate(device, ifaces, &s, testTime())
		if res.OK || !strings.Contains(res.Error, "не менее 8") {
			t.Errorf("ok=%v err=%q", res.OK, res.Error)
		}
	})
	t.Run("DNS-сервер не IP", func(t *testing.T) {
		s := fullSettings()
		s.DNS.Servers = "not-an-ip"
		res := Generate(device, ifaces, &s, testTime())
		if res.OK || !strings.Contains(res.Error, "DNS:") {
			t.Errorf("ok=%v err=%q", res.OK, res.Error)
		}
	})
	t.Run("пул вне подсети", func(t *testing.T) {
		s := fullSettings()
		s.DHCP.PoolStart = "10.0.0.5"
		s.DHCP.PoolEnd = "10.0.0.50"
		res := Generate(device, ifaces, &s, testTime())
		if res.OK || !strings.Contains(res.Error, "внутри подсети") {
			t.Errorf("ok=%v err=%q", res.OK, res.Error)
		}
	})
	t.Run("DHCP без LAN", func(t *testing.T) {
		s := fullSettings()
		s.LAN.Enabled = false
		res := Generate(device, ifaces, &s, testTime())
		if res.OK || !strings.Contains(res.Error, "Addresses") {
			t.Errorf("ok=%v err=%q", res.OK, res.Error)
		}
	})
}

func TestGenerate_NothingEnabled(t *testing.T) {
	device := deviceWith("system")
	res := Generate(device, nil, &Settings{}, testTime())
	if !res.OK {
		t.Fatalf("err=%q", res.Error)
	}
	if !strings.Contains(res.Script, "Настройки не выбраны") {
		t.Errorf("скрипт:\n%s", res.Script)
	}
	if strings.Contains(res.Script, "/ip address add") {
		t.Error("выключенные разделы не должны генерироваться")
	}
}

func TestGenerate_DerivedShown(t *testing.T) {
	device := deviceWith("system", "dhcp")
	s := Settings{
		LAN:  LanSettings{Enabled: true, Interface: "ether2", Address: "192.168.88.1/24"},
		DHCP: DHCPSettings{Enabled: true, Interface: "ether2", LeaseTime: "1d"},
	}
	res := Generate(device, []Iface{{Name: "ether2", Type: "ethernet"}}, &s, testTime())
	if !res.OK {
		t.Fatalf("err=%q", res.Error)
	}
	if res.Derived == nil || res.Derived.Pool != "192.168.88.2-192.168.88.254" ||
		res.Derived.Network != "192.168.88.0/24" || res.Derived.Gateway != "192.168.88.1" {
		t.Errorf("derived = %+v", res.Derived)
	}
}

func TestRosQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain-name_1.2", "plain-name_1.2"},
		{"192.168.88.1/24", "192.168.88.1/24"},
		{"8.8.8.8,8.8.4.4", "8.8.8.8,8.8.4.4"},
		{"My Home", `"My Home"`},
		{"", `""`},
		{`say "hi"`, `"say \"hi\""`},
	}
	for _, c := range cases {
		if got := rosQuote(c.in); got != c.want {
			t.Errorf("rosQuote(%q) = %s, ожидалось %s", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Интерфейсы
// ---------------------------------------------------------------------------

func TestCollectInterfacesAPI(t *testing.T) {
	port, _, stop := startScriptMock(t)
	defer stop()

	list, err := CollectInterfaces(context.Background(), Params{
		Host: "127.0.0.1", APIPort: port, User: mockUser, Password: mockPassword, Protocol: "api",
	})
	if err != nil {
		t.Fatalf("CollectInterfaces: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("интерфейсов: %d, ожидалось 4", len(list))
	}
	if list[0].Name != "ether1" || list[0].Type != "ethernet" || list[0].Comment != "WAN" {
		t.Errorf("ether1: %+v", list[0])
	}
	if list[1].Name != "ether2" || list[1].Bridge != "bridge" {
		t.Errorf("bridge member: %+v", list[1])
	}
	var wlan *Iface
	for i := range list {
		if list[i].Name == "wlan1" {
			wlan = &list[i]
		}
	}
	if wlan == nil || !wlan.Disabled {
		t.Errorf("wlan1: %+v", wlan)
	}
}

func TestCollectInterfacesFailsClosedWhenBridgePortsCannotBeRead(t *testing.T) {
	port, _, stop := startScriptMockWithErrors(t, "", "not permitted")
	defer stop()
	_, err := CollectInterfaces(context.Background(), Params{
		Host: "127.0.0.1", APIPort: port, User: mockUser, Password: mockPassword, Protocol: "api",
	})
	if err == nil || !strings.Contains(err.Error(), "портов bridge") {
		t.Fatalf("missing bridge read error: %v", err)
	}
}

func TestCollectInterfacesREST(t *testing.T) {
	port, _, stop := startScriptRESTMock(t)
	defer stop()

	list, err := CollectInterfaces(context.Background(), Params{
		Host: "127.0.0.1", RESTPort: port, User: mockUser, Password: mockPassword, Protocol: "rest",
	})
	if err != nil {
		t.Fatalf("CollectInterfaces: %v", err)
	}
	if len(list) != 3 || list[2].Name != "wifi1" || list[2].Type != "wifi" {
		t.Errorf("list = %+v", list)
	}
	if len(list) > 1 && list[1].Bridge != "bridge" {
		t.Errorf("bridge member: %+v", list[1])
	}
}

func TestRejectBridgeMembersForL3(t *testing.T) {
	device := deviceWith("routeros")
	ifaces := []Iface{{Name: "ether1", Type: "ethernet", Bridge: "bridge"}, {Name: "ether2", Type: "ethernet", Bridge: "bridge"}, {Name: "bridge", Type: "bridge"}}
	cases := []struct {
		name string
		s    Settings
	}{
		{"address", Settings{LAN: LanSettings{Enabled: true, Interface: "ether2", Address: "192.168.88.1/24"}}},
		{"server", Settings{LAN: LanSettings{Enabled: true, Interface: "bridge", Address: "192.168.88.1/24"}, DHCP: DHCPSettings{Enabled: true, Interface: "ether2", LeaseTime: "1d"}}},
		{"wan", Settings{WAN: WANSettings{Enabled: true, Links: []WANLink{{Interface: "ether1", Mode: "dhcp", Distance: 1}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Generate(device, ifaces, &tc.s, testTime())
			if r.OK || !strings.Contains(r.Error, "входит в bridge") {
				t.Fatalf("bridge member accepted: %+v", r)
			}
		})
	}
	good := Settings{LAN: LanSettings{Enabled: true, Interface: "bridge", Address: "192.168.88.1/24"}, DHCP: DHCPSettings{Enabled: true, Interface: "bridge", LeaseTime: "1d"}}
	if r := Generate(device, ifaces, &good, testTime()); !r.OK {
		t.Fatalf("bridge L3 should be accepted: %s", r.Error)
	}
}

func TestDisabledPackageIsNotAvailable(t *testing.T) {
	dev := &Info{RouterOS: "6.49", Packages: []Package{{Name: "system"}, {Name: "dhcp", Disabled: true}}}
	for _, f := range AnalyzeFeatures(dev, nil) {
		if f.ID == "dhcp" && f.Available {
			t.Fatal("disabled DHCP package exposed as available")
		}
	}
}

func TestWiFiSpecificPackageTakesPrecedence(t *testing.T) {
	ok, name := hasWiFiPackage(map[string]bool{"routeros": true, "wifi-qcom-ac": true}, true)
	if !ok || name != "wifi-qcom-ac" {
		t.Fatalf("specific WiFi package lost: %v %s", ok, name)
	}
}

func TestBridgePortIncompleteDataIsRejected(t *testing.T) {
	if err := addBridgePorts([]Iface{{Name: "ether2"}}, []map[string]string{{"interface": "ether2"}}); err == nil {
		t.Fatal("incomplete topology accepted")
	}
}

// ---------------------------------------------------------------------------
// Применение скрипта
// ---------------------------------------------------------------------------

func TestApplyAPI(t *testing.T) {
	port, rec, stop := startScriptMock(t)
	defer stop()

	script := "# тест\n/ip address add address=192.168.88.1/24 interface=ether2"
	proto, err := ApplyScript(context.Background(), Params{
		Host: "127.0.0.1", APIPort: port, User: mockUser, Password: mockPassword, Protocol: "api",
	}, script)
	if err != nil {
		t.Fatalf("ApplyScript: %v", err)
	}
	if proto != "api" {
		t.Errorf("protocol = %q", proto)
	}
	// remove игнорируется, add содержит source, run выполняется с number.
	if !rec.contains("/system/script/add | =name=" + applyScriptName + " | =source=" + script) {
		t.Errorf("add не записан: %+v", rec.cmds)
	}
	if !rec.contains("/system/script/run | =number=" + applyScriptName) {
		t.Errorf("run не записан: %+v", rec.cmds)
	}
}

func TestApplyRunErrorDoesNotRetryOrFallBack(t *testing.T) {
	apiPort, apiRec, stopAPI := startScriptMockWithRunError(t, "failure: can not run on slave interface")
	defer stopAPI()
	restPort, restRec, stopREST := startScriptRESTMock(t)
	defer stopREST()

	_, err := ApplyScript(context.Background(), Params{
		Host: "127.0.0.1", APIPort: apiPort, RESTPort: restPort,
		User: mockUser, Password: mockPassword, Protocol: "auto",
	}, "/log info test")
	if err == nil || !strings.Contains(err.Error(), "can not run on slave interface") || !strings.Contains(err.Error(), "частично") {
		t.Fatalf("unexpected error: %v", err)
	}
	if apiRec.contains("/system/script/run | =numbers=") {
		t.Fatal("execution retried with numbers after script error")
	}
	if restRec.contains("/rest/system/script") {
		t.Fatal("execution attempted again over REST after script error")
	}
}

func TestApplyAPI_RemovesOldScriptFirst(t *testing.T) {
	port, rec, stop := startScriptMock(t)
	defer stop()

	_, err := ApplyScript(context.Background(), Params{
		Host: "127.0.0.1", APIPort: port, User: mockUser, Password: mockPassword, Protocol: "api",
	}, "/log info test")
	if err != nil {
		t.Fatalf("ApplyScript: %v", err)
	}
	idxRemove, idxAdd, idxRun := -1, -1, -1
	for i, c := range rec.cmds {
		if strings.HasPrefix(c, "/system/script/remove") {
			idxRemove = i
		}
		if strings.HasPrefix(c, "/system/script/add") {
			idxAdd = i
		}
		if strings.HasPrefix(c, "/system/script/run") {
			idxRun = i
		}
	}
	if !(idxRemove >= 0 && idxRemove < idxAdd && idxAdd < idxRun) {
		t.Errorf("неверный порядок команд: %+v", rec.cmds)
	}
}

func TestApplyREST(t *testing.T) {
	port, rec, stop := startScriptRESTMock(t)
	defer stop()

	proto, err := ApplyScript(context.Background(), Params{
		Host: "127.0.0.1", RESTPort: port, User: mockUser, Password: mockPassword, Protocol: "rest",
	}, "/ip dns set servers=8.8.8.8")
	if err != nil {
		t.Fatalf("ApplyScript: %v", err)
	}
	if proto != "rest" {
		t.Errorf("protocol = %q", proto)
	}
	if !rec.contains(`POST /rest/system/script/run {"number":"` + applyScriptName + `"}`) {
		t.Errorf("run не записан: %+v", rec.cmds)
	}
	if !rec.contains(`"source":"/ip dns set servers=8.8.8.8"`) {
		t.Errorf("add не записан: %+v", rec.cmds)
	}
}

func TestApplyScript_Empty(t *testing.T) {
	_, err := ApplyScript(context.Background(), Params{Host: "127.0.0.1", User: "a"}, "   ")
	if err == nil {
		t.Fatal("ожидалась ошибка для пустого скрипта")
	}
}

// ---------------------------------------------------------------------------
// Приведение IP-адресов к формату
// ---------------------------------------------------------------------------

func TestNormalizeAddr(t *testing.T) {
	cases := []struct{ in, want string }{
		{"192.168.088.001/24", "192.168.88.1/24"},
		{" 192.168.001.010 ", "192.168.1.10"},
		{"10.0.0.300/40", "10.0.0.255/32"},
		{"192.168.1", "192.168.1"},
		{"1.2.3.4.5", "1.2.3.4"},
		{"192.168..1", "192.168..1"},
		{"", ""},
		{"abc192.168.1.1", "192.168.1.1"},
	}
	for _, c := range cases {
		if got := NormalizeAddr(c.in); got != c.want {
			t.Errorf("NormalizeAddr(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
	if got := NormalizeAddrList("008.008.008.008, 001.001.001.001"); got != "8.8.8.8,1.1.1.1" {
		t.Errorf("NormalizeAddrList = %q", got)
	}
}

// нормализация внутри Generate: «грязный» IP не должен ломать генерацию
func TestGenerate_NormalizesIPBeforeValidation(t *testing.T) {
	device := deviceWith("system", "dhcp")
	s := Settings{
		LAN:  LanSettings{Enabled: true, Interface: "ether2", Address: "192.168.088.001/24"},
		DHCP: DHCPSettings{Enabled: true, Interface: "ether2", LeaseTime: "1d"},
		DNS:  DNSSettings{Enabled: true, Servers: "008.008.008.008"},
	}
	res := Generate(device, []Iface{{Name: "ether2", Type: "ethernet"}}, &s, testTime())
	if !res.OK {
		t.Fatalf("err=%q", res.Error)
	}
	for _, w := range []string{
		"address=192.168.88.1/24",
		"ranges=192.168.88.2-192.168.88.254",
		"servers=8.8.8.8",
	} {
		if !strings.Contains(res.Script, w) {
			t.Errorf("нет %q в скрипте:\n%s", w, res.Script)
		}
	}
	// исходная структура не должна меняться
	if s.LAN.Address != "192.168.088.001/24" {
		t.Errorf("входные данные изменены: %q", s.LAN.Address)
	}
}

// Объединённый пакет routeros: все разделы доступны, Wi-Fi — если есть радио
// и интерфейс (типичный RouterOS 7 вида «routeros»).
func TestAnalyzeFeatures_CombinedPackage(t *testing.T) {
	dev := deviceWith("routeros")
	ifaces := []Iface{{Name: "ether1", Type: "ethernet"}, {Name: "wifi1", Type: "wifi"}}
	feats := AnalyzeFeatures(dev, ifaces)
	for _, id := range []string{featLAN, featDHCP, featDNS, featNTP, featPPPoE, featWiFi} {
		f := featureByID(t, feats, id)
		if !f.Available {
			t.Errorf("раздел %s должен быть доступен при пакете routeros (reason=%q)", id, f.Reason)
		}
	}
	if m := featureByID(t, feats, featWiFi).Mode; m != "wave2" {
		t.Errorf("режим Wi-Fi = %q, ожидался wave2", m)
	}
}

func TestAnalyzeFeatures_CombinedWithoutRadio(t *testing.T) {
	dev := deviceWith("routeros")
	feats := AnalyzeFeatures(dev, []Iface{{Name: "ether1", Type: "ethernet"}})
	w := featureByID(t, feats, featWiFi)
	if w.Available {
		t.Fatal("без беспроводных интерфейсов Wi-Fi должен быть недоступен")
	}
	if !strings.Contains(w.Reason, "нет беспроводных интерфейсов") {
		t.Errorf("reason = %q", w.Reason)
	}
}

func TestAnalyzeFeatures_WifiQcom(t *testing.T) {
	dev := deviceWith("system", "wifi-qcom")
	feats := AnalyzeFeatures(dev, []Iface{{Name: "wifi1", Type: "wifi"}})
	w := featureByID(t, feats, featWiFi)
	if !w.Available || w.Mode != "wave2" || w.Package != "wifi-qcom" {
		t.Errorf("wifi = %+v", w)
	}
}

func TestDHCPNetworkAndNTPRouterOS7Paths(t *testing.T) {
	s := fullSettings()
	s.WiFi.Enabled = false
	s.PPPoE.Enabled = false
	device := deviceWith("routeros")
	device.RouterOS = "7.22.3"
	res := Generate(device, nil, &s, testTime())
	if !res.OK {
		t.Fatal(res.Error)
	}
	for _, bad := range []string{"/ip dhcp-server-network", "/system ntp client set enabled=yes servers="} {
		if strings.Contains(res.Script, bad) {
			t.Errorf("устаревший путь или свойство: %s", bad)
		}
	}
	for _, want := range []string{"/ip dhcp-server network remove", "/ip dhcp-server network add", "/system ntp client servers find where address=pool.ntp.org", "/system ntp client servers add address=pool.ntp.org"} {
		if !strings.Contains(res.Script, want) {
			t.Errorf("нет строки %s", want)
		}
	}
}
