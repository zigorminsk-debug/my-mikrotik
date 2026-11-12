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
		for _, want := range []string{"routing-table=mti-probe", "blackhole", "chain=output protocol=icmp dst-address=8.8.4.4", "new-routing-mark=mti-probe", `\$now - \$mtiFailBad >= 30s`, `\$now - \$mtiFailGood >= 1m`, "interval=5s",
			":do { /ip firewall mangle move [find comment=mti-probe] 0 } on-error={",
			// В RouterOS 7 /ping при нуле ответов возвращает «ничего»:
			// значение можно брать только после проверки типа.
			"count=3 interval=1s", ":set rtype [:typeof \\$r]", `:if (\$rtype = \"num\") do={ :set replies \$r }`,
			// Основной канал управляется своим статическим маршрутом:
			// маршрут от DHCP-клиента не меняет distance на ходу.
			"/ip route add dst-address=0.0.0.0/0 gateway=\\$gw distance=\\$want comment=mti-wan-0",
			"/ip route set [find comment=mti-wan-0] distance=\\$distance",
			"mti-failover: backup=", "/ip firewall connection remove [find]",
			// Диагностика: состояние видно в журнале без ручных проверок.
				"mti-failover: состояние", "mti-failover: переключение не удалось",
				`:set mtiFailFix (\$mtiFailFix + 1);`, `:local hb 5m; :if (\$replies = 0) do={ :set hb 30s };`,
				// Строка состояния должна показывать не только счётчики, но и
				// фактический активный шлюз — по ней видно, почему трафик идёт
				// не туда, без ручных команд на роутере.
				`, активный=`, `, маршрутов=`,
				// Сторож резервного канала: после переподключения модема канал
				// поднимается сам, без ручного вмешательства.
				"mti-failover: резервный канал",
				`gateway~\$bk`, "/interface set [find name=ether2] disabled=yes",
				`/ip dhcp-client release [find where interface=\$bk]`,
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
		// Переключение через приоритет DHCP-клиента не работает: маршрут,
		// установленный клиентом, не меняет distance до обновления аренды.
		if strings.Contains(res.Script, `default-route-distance=\$distance`) {
			t.Errorf("%s: переключение опирается на default-route-distance", mode)
		}
		if mode == "dhcp" && !strings.Contains(res.Script, "add-default-route=no default-route-distance=250") {
			t.Error("dhcp: клиент не переведён на управляемый маршрут")
		}
		// Страховка нужна только там, где маршрут основного канала создаёт
		// скрипт: если создать его не удалось, канал не должен остаться без
		// маршрута вообще — это хуже, чем отсутствие резервирования.
		if mode != "static" {
			if !strings.Contains(res.Script, `mtiFailNoRoute`) {
				t.Errorf("%s: нет страховки на случай невозможности создать маршрут", mode)
			}
			if !strings.Contains(res.Script, "mti-failover: не удалось создать управляемый маршрут") {
				t.Errorf("%s: нет команды возврата маршрута каналу", mode)
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
