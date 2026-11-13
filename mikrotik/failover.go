package mikrotik

import (
	"fmt"
	"strings"
)

// buildFailover добавляет контроль интернета по основному каналу.
//
// Ключевое решение: основной канал получает статический маршрут по умолчанию,
// которым управляет скрипт (комментарий mti-wan-0), а DHCP-клиент переводится
// в add-default-route=no. Причина: маршрут, установленный DHCP-клиентом, не
// меняет distance на ходу — /ip dhcp-client set … default-route-distance=
// меняет настройку, но не сам маршрут до обновления аренды, поэтому
// переключение на резерв не происходило.
//
// Проба идёт ICMP роутера до контрольного узла, закреплённого за основным
// каналом отдельной таблицей маршрутизации mti-probe: после переключения
// трафик уходит через резерв, а проба продолжает проверять основной.
func buildFailover(b *strings.Builder, s WANSettings) {
	b.WriteString("\n# RouterOS 7: контроль интернета по основному каналу, 30 с до переключения, 60 с до возврата.\n")
	b.WriteString("/system scheduler remove [find name=mti-failover]\n/system script remove [find name=mti-failover]\n")
	b.WriteString("/ip firewall mangle remove [find comment=mti-probe]\n/ip route remove [find comment=mti-probe]\n/ip route remove [find comment=mti-probe-link]\n")
	if len(s.Links) == 0 {
		return
	}
	primary := s.Links[0]
	if primary.Mode == "usb-tether" {
		buildUSBFailover(b, s)
		return
	}
	upDistance := primary.Distance
	if upDistance < 1 {
		upDistance = 1
	}
	if !s.Failover {
		// Режим выключен: основной канал снова устанавливает маршрут сам,
		// наш управляемый маршрут удаляется.
		switch primary.Mode {
		case "dhcp":
			fmt.Fprintf(b, ":do { /ip dhcp-client set [find where interface=%s] add-default-route=yes default-route-distance=%d } on-error={};\n", rosQuote(primary.Interface), upDistance)
			b.WriteString("/ip route remove [find comment=mti-wan-0]\n")
		case "lte":
			fmt.Fprintf(b, ":do { /interface lte apn set [find where name=mti-wan-0-apn] add-default-route=yes default-route-distance=%d } on-error={};\n", upDistance)
			b.WriteString("/ip route remove [find comment=mti-wan-0]\n")
		}
		return
	}
	target := s.ProbeHost
	if target == "" {
		target = "8.8.8.8"
	}
	// На время аварии основной канал должен стать хуже резервного.
	downDistance := upDistance + 2
	if len(s.Links) > 1 && s.Links[1].Distance >= 1 {
		downDistance = s.Links[1].Distance + 1
	}
	if downDistance <= upDistance {
		downDistance = upDistance + 1
	}
	if downDistance > 255 {
		downDistance = 255
	}
	manageStatic := primary.Mode == "dhcp" || primary.Mode == "lte"
	// Команда возврата маршрута самому каналу — страховка: если создать
	// управляемый маршрут не удалось, основной канал не должен остаться
	// без маршрута вообще, иначе весь трафик уйдёт на резерв.
	restoreCmd := ""
	if manageStatic {
		if primary.Mode == "dhcp" {
			restoreCmd = fmt.Sprintf(":do { /ip dhcp-client set [find where interface=%s] add-default-route=yes default-route-distance=%d } on-error={}", rosQuote(primary.Interface), upDistance)
		} else {
			restoreCmd = fmt.Sprintf(":do { /interface lte apn set [find where name=mti-wan-0-apn] add-default-route=yes default-route-distance=%d } on-error={}", upDistance)
		}
		restoreCmd = restoreCmd + "; :log warning \"mti-failover: не удалось создать управляемый маршрут — основной канал оставляет свой\""
	}

	// Таблица и метка для пробы.
	b.WriteString(":if ([:len [/routing table find name=mti-probe]] = 0) do={ /routing table add name=mti-probe fib }\n")
	b.WriteString("/ip firewall mangle remove [find comment=mti-probe]\n/ip route remove [find comment=mti-probe]\n")
	b.WriteString("/ip route add blackhole dst-address=0.0.0.0/0 routing-table=mti-probe distance=254 comment=mti-probe\n")
	fmt.Fprintf(b, "/ip firewall mangle add chain=output protocol=icmp %s action=mark-routing new-routing-mark=mti-probe passthrough=no comment=mti-probe\n", attr("dst-address", target))
	// Порядок правила желателен, но не обязателен: если первым в цепочке стоит
	// встроенное правило, RouterOS не даёт поднять наше выше него
	// («cannot move builtin»). Такая ошибка не должна ронять весь скрипт.
	b.WriteString(":do { /ip firewall mangle move [find comment=mti-probe] 0 } on-error={ :log warning \"mti: правило mti-probe оставлено на месте — выше него встроенное правило\" }\n")

	// Основной канал переводим под управление скрипта: сначала создаём свой
	// маршрут (шлюз совпадает с полученным от провайдера, поэтому трафик не
	// прерывается), затем убираем маршрут, установленный DHCP-клиентом.
	if manageStatic {
		b.WriteString(":local mtiOk false;\n")
		switch primary.Mode {
		case "dhcp":
			fmt.Fprintf(b, `:do { :local c [/ip dhcp-client find where interface=%s]; :if ([:len $c] > 0) do={ :set c [:pick $c 0]; :if ([/ip dhcp-client get $c status] = "bound") do={ :local gwb [/ip dhcp-client get $c gateway]; :if ([:len $gwb] > 0) do={ /ip route remove [find comment=mti-wan-0]; /ip route add dst-address=0.0.0.0/0 gateway=($gwb . %s) distance=%d comment=mti-wan-0; :set mtiOk true } } } } on-error={};`+"\n", rosQuote(primary.Interface), rosQuote("%"+primary.Interface), upDistance)
			fmt.Fprintf(b, ":if ($mtiOk) do={ /ip dhcp-client set [find where interface=%s] add-default-route=no default-route-distance=250 };\n", rosQuote(primary.Interface))
		case "lte":
			// RouterOS can expose a dynamic DHCP client on LTE (e.g. USB
			// tethering). Use its lease gateway, not an on-link default route.
			// Native point-to-point LTE has no DHCP client and uses the interface.
			fmt.Fprintf(b, `:do { :local c [/ip dhcp-client find where interface=%s]; :local gwb ""; :if ([:len $c] > 0) do={ :set c [:pick $c 0]; :if ([/ip dhcp-client get $c status] = "bound") do={ :local g [/ip dhcp-client get $c gateway]; :if ([:len $g] > 0) do={ :set gwb ($g . %s) } } } else={ :if ([/interface get [find name=%s] running]) do={ :set gwb %s } }; :if ([:len $gwb] > 0) do={ /ip route remove [find comment=mti-wan-0]; /ip route add dst-address=0.0.0.0/0 gateway=$gwb distance=%d comment=mti-wan-0; :set mtiOk true } } on-error={};`+"\n", rosQuote(primary.Interface), rosQuote("%"+primary.Interface), rosQuote(primary.Interface), rosQuote(primary.Interface), upDistance)
			b.WriteString(":if ($mtiOk) do={ /interface lte apn set [find where name=mti-wan-0-apn] add-default-route=no default-route-distance=250 };\n")
		}
		// Страховка: если создать управляемый маршрут не удалось, возвращаем
		// маршрут самому каналу — пусть без резервирования, но с интернетом.
		fmt.Fprintf(b, ":if (!$mtiOk) do={ %s }\n", restoreCmd)
	}

	var code strings.Builder
	code.WriteString(":if ([/system script job print count-only as-value where script=[:jobname]] > 1) do={ :error \"mti-failover already running\" };\n:global mtiFailBad; :global mtiFailGood; :global mtiFailBackup; :global mtiFailSwitch; :global mtiFailNoGw; :global mtiFailLog; :global mtiFailFix; :global mtiFailRouteGw; :global mtiFailRouteDist; :global mtiFailNoRoute; :global mtiFailProbeGw;\n:local now [/system resource get uptime];\n:local gw \"\";\n")
	switch primary.Mode {
	case "static":
		fmt.Fprintf(&code, ":set gw %s;\n", rosQuote(primary.Gateway+"%"+primary.Interface))
	case "lte":
		fmt.Fprintf(&code, `:local c [/ip dhcp-client find where interface=%s];
:if ([:len $c] > 0) do={
:set c [:pick $c 0];
:if ([/ip dhcp-client get $c status] = "bound") do={ :local g [/ip dhcp-client get $c gateway]; :if ([:len $g] > 0) do={ :set gw ($g . %s) } };
} else={ :do { :if ([/interface get [find name=%s] running]) do={ :set gw %s } } on-error={} };
`, rosQuote(primary.Interface), rosQuote("%"+primary.Interface), rosQuote(primary.Interface), rosQuote(primary.Interface))
	case "dhcp":
		fmt.Fprintf(&code, ":local c [/ip dhcp-client find where interface=%s];\n:if ([:len $c] > 0) do={ :set c [:pick $c 0]; :if ([/ip dhcp-client get $c status] = \"bound\") do={\n", rosQuote(primary.Interface))
		fmt.Fprintf(&code, ":local g [/ip dhcp-client get $c gateway]; :if ([:len $g] > 0) do={ :set gw ($g . %s) };\n} };\n", rosQuote("%"+primary.Interface))
	}
	// Текущее состояние переключения: false — основной, true — резерв.
	code.WriteString(":if ([:typeof $mtiFailBackup] = \"nothing\") do={ :set mtiFailBackup false; :do { :set mtiFailBackup ([/ip route get [find comment=mti-wan-0] distance] > 1) } on-error={ :set mtiFailBackup false } };\n")
	fmt.Fprintf(&code, ":local want %d; :if ($mtiFailBackup) do={ :set want %d };\n", upDistance, downDistance)
	code.WriteString(":if ([:typeof $mtiFailNoGw] = \"nothing\") do={ :set mtiFailNoGw false };\n")
	// Без действующего DHCP-шлюза старый маршрут может оставаться активным,
	// даже когда lease потерян. Отключаем свои маршруты до получения шлюза.
	code.WriteString(":if ($gw = \"\") do={\n:if (!$mtiFailNoGw) do={ :log warning \"mti-failover: нет шлюза основного канала — проба пропущена\"; :set mtiFailNoGw true };\n")
	if manageStatic {
		// A lost lease can leave an Ethernet-like LTE interface running. Do
		// not keep a stale default route active or allow probes to use it.
		code.WriteString(":do { /ip route disable [find where comment=mti-wan-0 and disabled=no]; /ip route disable [find where comment=mti-probe-link and disabled=no] } on-error={}; :set mtiFailBad; :set mtiFailGood; :set mtiFailBackup true; :set mtiFailSwitch;\n")
	}
	code.WriteString("} else={\n:set mtiFailNoGw false;\n")
	// Маршрут основного канала создаём и обновляем только при реальном
	// изменении, иначе каждые 5 секунд в аудите появляется лишняя запись.
	// Для канала со статическим адресом страховка не нужна: маршрут задан
	// вручную, и скрипт его не создаёт сам.
	if manageStatic {
		fmt.Fprintf(&code, ":local routeOK false;\n:local r0 [/ip route find comment=mti-wan-0];\n:if ([:len $r0] = 0) do={\n:do { /ip route add dst-address=0.0.0.0/0 gateway=$gw distance=$want comment=mti-wan-0; :set mtiFailRouteGw $gw; :set mtiFailRouteDist $want; :set routeOK true; :log warning (\"mti-failover: создан маршрут основного канала, приоритет \" . $want) } on-error={ :log warning \"mti-failover: не удалось создать маршрут основного канала\" };\n:if ([:typeof $mtiFailNoRoute] = \"nothing\") do={ :set mtiFailNoRoute $now };\n:if (($now - $mtiFailNoRoute) >= 60s) do={ %s; :set mtiFailNoRoute; };\n} else={\n:set mtiFailNoRoute;\n:if (([:typeof $mtiFailRouteGw] = \"nothing\") || ($mtiFailRouteGw != $gw) || ([:typeof $mtiFailRouteDist] = \"nothing\") || ($mtiFailRouteDist != $want) || ([:tostr [/ip route get [:pick $r0 0] gateway]] != $gw) || ([/ip route get [:pick $r0 0] distance] != $want) || ([/ip route get [:pick $r0 0] disabled])) do={\n:do { /ip route set $r0 gateway=$gw distance=$want disabled=no; :set mtiFailRouteGw $gw; :set mtiFailRouteDist $want; :set routeOK true; :log warning (\"mti-failover: маршрут основного канала обновлён, приоритет \" . $want) } on-error={ :log warning \"mti-failover: не удалось обновить маршрут основного канала\" };\n} else={ :set routeOK true };\n};\n", restoreCmd)
	} else {
		code.WriteString(":local r0 [/ip route find comment=mti-wan-0];\n:if ([:len $r0] = 0) do={\n:do { /ip route add dst-address=0.0.0.0/0 gateway=$gw distance=$want comment=mti-wan-0; :set mtiFailRouteGw $gw; :set mtiFailRouteDist $want; :log warning (\"mti-failover: создан маршрут основного канала, приоритет \" . $want) } on-error={ :log warning \"mti-failover: не удалось создать маршрут основного канала\" };\n\n} else={\n:if (([:typeof $mtiFailRouteGw] = \"nothing\") || ($mtiFailRouteGw != $gw) || ([:typeof $mtiFailRouteDist] = \"nothing\") || ($mtiFailRouteDist != $want) || ([:tostr [/ip route get [:pick $r0 0] gateway]] != $gw) || ([/ip route get [:pick $r0 0] distance] != $want) || ([/ip route get [:pick $r0 0] disabled])) do={\n:do { /ip route set $r0 gateway=$gw distance=$want disabled=no; :set mtiFailRouteGw $gw; :set mtiFailRouteDist $want; :log warning (\"mti-failover: маршрут основного канала обновлён, приоритет \" . $want) } on-error={ :log warning \"mti-failover: не удалось обновить маршрут основного канала\" };\n};\n};\n")
	}
	if manageStatic {
		code.WriteString(":if ($routeOK) do={\n")
		if primary.Mode == "dhcp" {
			fmt.Fprintf(&code, ":do { :local c [/ip dhcp-client find where interface=%s]; :if (([:len $c] > 0) && ([/ip dhcp-client get [:pick $c 0] add-default-route] != \"no\")) do={ /ip dhcp-client set [:pick $c 0] add-default-route=no default-route-distance=250 } } on-error={};\n", rosQuote(primary.Interface))
		} else {
			code.WriteString(":do { :local a [/interface lte apn find where name=mti-wan-0-apn]; :if (([:len $a] > 0) && ([/interface lte apn get [:pick $a 0] add-default-route] = true)) do={ /interface lte apn set [:pick $a 0] add-default-route=no default-route-distance=250 } } on-error={};\n")
		}
		code.WriteString("};\n")
	}
	// Проба: отдельный маршрут в таблице mti-probe ведёт строго в основной
	// канал, поэтому после переключения проверка не идёт через резерв.
	code.WriteString(":local pr [/ip route find comment=mti-probe-link];\n:if ([:len $pr] = 0) do={ /ip route add dst-address=0.0.0.0/0 gateway=$gw routing-table=mti-probe distance=1 comment=mti-probe-link; :set mtiFailProbeGw $gw } else={\n:if (([:typeof $mtiFailProbeGw] = \"nothing\") || ($mtiFailProbeGw != $gw) || ([:tostr [/ip route get [:pick $pr 0] gateway]] != $gw) || ([/ip route get [:pick $pr 0] disabled])) do={ /ip route set $pr gateway=$gw disabled=no; :set mtiFailProbeGw $gw };\n};\n")
	// В RouterOS 7 /ping при полном отсутствии ответов возвращает «ничего»
	// (в v6 — 0): переменная от такого присваивания очищается, и проверка
	// «$replies = 0» всегда ложна — канал кажется живым при любой аварии.
	// Поэтому значение берём только если это число, иначе ответов нет.
	// Три попытки вместо одной: одиночный ping на канале с потерями
	// (встречаются 33 %) раньше давал ложную аварию.
	code.WriteString(":local replies 0; :local rtype \"nothing\";\n")
	fmt.Fprintf(&code, ":do { :local r [/ping address=%s count=3 interval=1s]; :set rtype [:typeof $r]; :if ($rtype = \"num\") do={ :set replies $r } } on-error={ :set replies 0; :set rtype \"error\" };\n", rosQuote(target))
	code.WriteString(":if ([:typeof $mtiFailBackup] = \"nothing\") do={ :set mtiFailBackup false };\n:local change false; :local next $mtiFailBackup;\n")
	fmt.Fprintf(&code, ":if ($replies = 0) do={\n:set mtiFailGood;\n:if ([:typeof $mtiFailBad] = \"nothing\") do={ :set mtiFailBad $now; :log warning (\"mti-failover: нет ответа от %s (0 из 3, тип=\" . $rtype . \"), отсчёт 30 c\") };\n", target)
	code.WriteString(":if (($now - $mtiFailBad >= 30s) && (!$mtiFailBackup)) do={ :set next true; :set change true };\n} else={\n:set mtiFailBad;\n")
	fmt.Fprintf(&code, ":if ([:typeof $mtiFailGood] = \"nothing\") do={ :set mtiFailGood $now; :log warning (\"mti-failover: ответ от %s получен (\" . $replies . \" из 3), отсчёт 60 c\") };\n", target)
	code.WriteString(":if (($now - $mtiFailGood >= 1m) && $mtiFailBackup) do={ :set next false; :set change true };\n};\n")
	// Переключение: меняем приоритет своего статического маршрута — в отличие
	// от маршрута DHCP-клиента он меняется сразу и предсказуемо.
	code.WriteString(":if ($change) do={\n")
	fmt.Fprintf(&code, ":local distance %d; :if ($next) do={ :set distance %d };\n", upDistance, downDistance)
	code.WriteString(":do { /ip route set [find comment=mti-wan-0] distance=$distance; :set mtiFailRouteDist $distance; :set mtiFailBackup $next; :set mtiFailSwitch $now; :do { /ip firewall connection remove [find] } on-error={}; :log warning (\"mti-failover: backup=\" . $mtiFailBackup . \", приоритет основного канала \" . $distance) } on-error={ :log warning \"mti-failover: не удалось изменить приоритет основного канала\" };\n};\n")
	// Строка состояния: раз в 5 минут, а во время аварии — раз в 30 секунд.
	code.WriteString(":local hb 5m; :if ($replies = 0) do={ :set hb 30s };\n:if (([:typeof $mtiFailLog] = \"nothing\") || (($now - $mtiFailLog) >= $hb)) do={\n:set mtiFailLog $now;\n:local actGw \"-\"; :do { :local ai [/ip route find where dst-address=0.0.0.0/0 and routing-table=main and active]; :if ([:len $ai] > 0) do={ :set actGw [/ip route get [:pick $ai 0] gateway] } } on-error={};\n:local allCnt 0; :do { :set allCnt [:len [/ip route find where dst-address=0.0.0.0/0]] } on-error={};\n:local dynCnt 0; :do { :set dynCnt [:len [/ip route find where dynamic and dst-address=0.0.0.0/0]] } on-error={};\n:local curDist \"-\"; :do { :set curDist [/ip route get [find comment=mti-wan-0] distance] } on-error={};\n:log info (\"mti-failover: состояние — ответов \" . $replies . \" из 3, backup=\" . $mtiFailBackup . \", приоритет=\" . $curDist . \", активный=\" . $actGw . \", маршрутов=\" . $allCnt . \", из них динамических \" . $dynCnt);\n")
	code.WriteString("};\n};\n")
	// Do not restart or release the backup interface automatically: an
	// inactive DHCP route normally has an IP gateway, not an interface name.
	// A false alarm here would interrupt the only working connection.
	// Проверка после переключения: сверяем фактический приоритет маршрута и,
	// если он не применился, повторяем — но не больше двух раз за аварию.
	fmt.Fprintf(&code, ":if ([:typeof $mtiFailSwitch] != \"nothing\") do={\n:if (($now - $mtiFailSwitch) >= 10s) do={\n:local need %d; :if ($mtiFailBackup) do={ :set need %d };\n:local cur -1;\n:do { :set cur [/ip route get [find comment=mti-wan-0] distance] } on-error={ :set cur -1 };\n:if ([:typeof $mtiFailFix] = \"nothing\") do={ :set mtiFailFix 0 };\n:if (($cur < 0) || ($cur = $need)) do={\n:set mtiFailSwitch; :set mtiFailFix 0;\n} else={\n:set mtiFailFix ($mtiFailFix + 1);\n:if ($mtiFailFix < 3) do={\n:log warning (\"mti-failover: приоритет основного канала остался \" . $cur . \" вместо \" . $need . \", повторяю\");\n:do { /ip route set [find comment=mti-wan-0] distance=$need } on-error={};\n:set mtiFailSwitch $now;\n} else={\n:log warning (\"mti-failover: переключение не удалось: приоритет остался \" . $cur . \" вместо \" . $need);\n:set mtiFailSwitch;\n};\n};\n};\n};\n", upDistance, downDistance)
	b.WriteString(":global mtiFailBad; :set mtiFailBad; :global mtiFailGood; :set mtiFailGood; :global mtiFailBackup; :set mtiFailBackup false; :global mtiFailSwitch; :set mtiFailSwitch; :global mtiFailNoGw; :set mtiFailNoGw false; :global mtiFailLog; :set mtiFailLog; :global mtiFailFix; :set mtiFailFix 0; :global mtiFailRouteGw; :set mtiFailRouteGw; :global mtiFailRouteDist; :set mtiFailRouteDist; :global mtiFailNoRoute; :set mtiFailNoRoute; :global mtiFailProbeGw; :set mtiFailProbeGw;\n")
	fmt.Fprintf(b, "/system script add name=mti-failover policy=read,write,policy,test source=%s\n", rosQuote(code.String()))
	b.WriteString("/system scheduler add name=mti-failover interval=5s start-time=startup on-event=mti-failover policy=read,write,policy,test\n")
}
