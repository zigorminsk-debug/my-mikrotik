package mikrotik

import (
	"fmt"
	"strings"
)

// A dedicated output routing mark keeps router-originated probes off the backup.
// A blackhole default prevents fallback when the primary has no usable gateway.
func buildFailover(b *strings.Builder, s WANSettings) {
	b.WriteString("\n# RouterOS 7: ICMP probes pinned to primary, 30s down / 60s recovery.\n")
	b.WriteString("/system scheduler remove [find name=mti-failover]\n/system script remove [find name=mti-failover]\n")
	b.WriteString("/ip firewall mangle remove [find comment=mti-probe]\n/ip route remove [find comment=mti-probe]\n/ip route remove [find comment=mti-probe-link]\n")
	if !s.Failover {
		return
	}
	primary := s.Links[0]
	target := s.ProbeHost
	if target == "" {
		target = "8.8.8.8"
	}
	// На время аварии основной канал должен стать хуже резервного, поэтому
	// аварийный приоритет считается от приоритета резервного канала, а не
	// задаётся числом: при приоритетах 1 и 2 это 1 и 3.
	upDistance := primary.Distance
	if upDistance < 1 {
		upDistance = 1
	}
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
	b.WriteString(":if ([:len [/routing table find name=mti-probe]] = 0) do={ /routing table add name=mti-probe fib }\n")
	b.WriteString("/ip firewall mangle remove [find comment=mti-probe]\n/ip route remove [find comment=mti-probe]\n")
	b.WriteString("/ip route add blackhole dst-address=0.0.0.0/0 routing-table=mti-probe distance=254 comment=mti-probe\n")
	fmt.Fprintf(b, "/ip firewall mangle add chain=output protocol=icmp %s action=mark-routing new-routing-mark=mti-probe passthrough=no comment=mti-probe\n", attr("dst-address", target))
	// Порядок правила желателен, но не обязателен: если первым в цепочке стоит
	// встроенное правило, RouterOS не даёт поднять наше выше него
	// («cannot move builtin»). Такая ошибка не должна ронять весь скрипт —
	// иначе применение падает целиком из-за несущественной детали.
	b.WriteString(":do { /ip firewall mangle move [find comment=mti-probe] 0 } on-error={ :log warning \"mti: правило mti-probe оставлено на месте — выше него встроенное правило\" }\n")
	var code strings.Builder
	code.WriteString(":global mtiFailBad; :global mtiFailGood; :global mtiFailBackup; :global mtiFailSwitch; :global mtiFailNoGw;\n:local now [/system resource get uptime];\n:local gw \"\";\n")
	switch primary.Mode {
	case "static":
		fmt.Fprintf(&code, ":set gw %s;\n", rosQuote(primary.Gateway+"%"+primary.Interface))
	case "lte":
		fmt.Fprintf(&code, ":if ([/interface get [find name=%s] running]) do={ :set gw %s };\n", rosQuote(primary.Interface), rosQuote(primary.Interface))
	case "dhcp":
		code.WriteString(":local c [/ip dhcp-client find where interface=" + rosQuote(primary.Interface) + "];\n:if ([:len $c] > 0) do={ :if ([/ip dhcp-client get $c status] = \"bound\") do={\n")
		fmt.Fprintf(&code, ":set gw ([/ip dhcp-client get $c gateway] . %s);\n} };\n", rosQuote("%"+primary.Interface))
	}
	var distanceRead, distanceCur, setCmd, renewCmd string
	switch primary.Mode {
	case "static":
		distanceRead = "[/ip route get [find comment=mti-wan-0] distance]"
		distanceCur = ":do { :local r [/ip route find comment=mti-wan-0]; :if ([:len $r] > 0) do={ :set cur [/ip route get [:pick $r 0] distance] } } on-error={ :set cur -1 };"
		setCmd = "/ip route set [find comment=mti-wan-0] distance=$distance;"
	case "dhcp":
		distanceRead = "[/ip dhcp-client get [find where interface=" + rosQuote(primary.Interface) + "] default-route-distance]"
		distanceCur = ":do { :local r [/ip route find where dst-address=0.0.0.0/0 and gateway~\"" + primary.Interface + "\" and comment!=\"mti-probe-link\"]; :if ([:len $r] > 0) do={ :set cur [/ip route get [:pick $r 0] distance] } } on-error={ :set cur -1 };"
		setCmd = "/ip dhcp-client set [find where interface=" + rosQuote(primary.Interface) + "] default-route-distance=$distance;"
		renewCmd = ":do { /ip dhcp-client renew [find where interface=" + rosQuote(primary.Interface) + "] } on-error={ :log warning \"mti-failover: не удалось обновить аренду DHCP\" };"
	case "lte":
		distanceRead = "[/interface lte apn get [find name=mti-wan-0-apn] default-route-distance]"
		distanceCur = ":do { :local r [/interface lte apn find where name=mti-wan-0-apn]; :if ([:len $r] > 0) do={ :set cur [/interface lte apn get [:pick $r 0] default-route-distance] } } on-error={ :set cur -1 };"
		setCmd = "/interface lte apn set [find name=mti-wan-0-apn] default-route-distance=$distance;"
	}
	fmt.Fprintf(&code, ":if ([:typeof $mtiFailBackup] = \"nothing\") do={ :set mtiFailBackup false; :do { :set mtiFailBackup (%s > 1) } on-error={ :set mtiFailBackup false } };\n", distanceRead)
	code.WriteString(":if ([:typeof $mtiFailNoGw] = \"nothing\") do={ :set mtiFailNoGw false };\n")
	// Без шлюза проба бессмысленна. Если канал физически упал, маршрут по
	// умолчанию исчезает сам и резерв поднимается без скрипта, поэтому здесь
	// состояние не меняем. Прежняя версия гасила маршрут пробы при любом
	// пустом чтении шлюза: проба уходила в blackhole и живой канал
	// выглядел мёртвым (в журнале это были /ip route set ... disabled=yes).
	code.WriteString(":if ($gw = \"\") do={\n:if (!$mtiFailNoGw) do={ :log warning \"mti-failover: нет шлюза основного канала, проба пропущена\"; :set mtiFailNoGw true };\n} else={\n:set mtiFailNoGw false;\n")
	code.WriteString(":local route [/ip route find comment=mti-probe-link];\n:if ([:len $route] = 0) do={ /ip route add dst-address=0.0.0.0/0 gateway=$gw routing-table=mti-probe distance=1 comment=mti-probe-link } else={ /ip route set $route gateway=$gw disabled=no };\n")
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
	fmt.Fprintf(&code, ":if ($change) do={\n:local distance %d; :if ($next) do={ :set distance %d };\n", upDistance, downDistance)
	fmt.Fprintf(&code, ":do { %s } on-error={ :log warning \"mti-failover: не удалось изменить приоритет основного канала\" };\n", setCmd)
	// Сбрасываем таблицу соединений: иначе уже установленные сессии ещё
	// долго идут через старый канал, и переключение незаметно для пользователей.
	code.WriteString(":set mtiFailBackup $next;\n:set mtiFailSwitch $now;\n:do { /ip firewall connection remove [find] } on-error={};\n:log warning (\"mti-failover: backup=\" . $mtiFailBackup . \", приоритет основного канала \" . $distance);\n};\n};\n")
	// Проверка после переключения: маршрут, созданный DHCP-клиентом, иногда
	// не принимает новую distance до обновления аренды. Через 10 с сверяем
	// фактическое значение и, если оно не совпало, обновляем аренду.
	hint := ""
	if renewCmd != "" {
		hint = ", обновляю аренду DHCP"
	}
	fmt.Fprintf(&code, ":if ([:typeof $mtiFailSwitch] != \"nothing\") do={\n:if (($now - $mtiFailSwitch) >= 10s) do={\n:local want %d; :if ($mtiFailBackup) do={ :set want %d };\n:local cur -1;\n%s\n:if (($cur >= 0) && ($cur != $want)) do={\n:log warning (\"mti-failover: приоритет основного канала остался \" . $cur . \" вместо \" . $want . \"%s\");\n%s\n};\n:set mtiFailSwitch;\n};\n};\n", upDistance, downDistance, distanceCur, hint, renewCmd)
	b.WriteString(":global mtiFailBad; :set mtiFailBad; :global mtiFailGood; :set mtiFailGood; :global mtiFailBackup; :set mtiFailBackup false; :global mtiFailSwitch; :set mtiFailSwitch; :global mtiFailNoGw; :set mtiFailNoGw false;\n")
	fmt.Fprintf(b, "/system script add name=mti-failover policy=read,write,test source=%s\n", rosQuote(code.String()))
	b.WriteString("/system scheduler add name=mti-failover interval=5s start-time=startup on-event=mti-failover policy=read,write,test\n")
}
