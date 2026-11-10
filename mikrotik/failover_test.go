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
		// Перенос правила в начало цепочки не должен ронять весь скрипт.
		for _, want := range []string{"routing-table=mti-probe", "blackhole", "chain=output protocol=icmp dst-address=8.8.4.4", "new-routing-mark=mti-probe", `\$now - \$mtiFailBad >= 30s`, `\$now - \$mtiFailGood >= 1m`, "interval=5s", "default-route-distance=",
			":do { /ip firewall mangle move [find comment=mti-probe] 0 } on-error={",
			// В RouterOS 7 /ping при нуле ответов возвращает «ничего»:
			// значение можно брать только после проверки типа.
			"count=3 interval=1s", ":set rtype [:typeof \\$r]", `:if (\$rtype = \"num\") do={ :set replies \$r }`,
			"mti-failover: backup=", "/ip firewall connection remove [find]",
		} {
			if !strings.Contains(res.Script, want) {
				t.Errorf("%s missing %s", mode, want)
			}
		}
		// Присваивание результата /ping напрямую очищает переменную —
		// канал при этом всегда считается живым и переключения не бывает.
		if strings.Contains(res.Script, ":set replies [/ping") {
			t.Errorf("%s: прямой результат /ping присваивается переменной", mode)
		}
		if mode == "dhcp" && !strings.Contains(res.Script, `/ip dhcp-client renew [find where interface=ether1]`) {
			t.Error("dhcp: нет обновления аренды, если distance маршрута не применился")
		}
	}
}

func TestFailoverValidation(t *testing.T) {
	s := WANSettings{Failover: true, ProbeHost: "bad", Links: []WANLink{{Mode: "dhcp", Interface: "ether1", Distance: 1}}}
	if len(validateWAN(s, PPPoESettings{})) < 2 {
		t.Fatal("single provider and invalid host accepted")
	}
}
