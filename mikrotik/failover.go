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
	code.WriteString(":global mtiFailBad; :global mtiFailGood; :global mtiFailBackup;\n:local now [/system resource get uptime];\n:local gw \"\";\n")
	switch primary.Mode {
	case "static":
		fmt.Fprintf(&code, ":set gw %s;\n", rosQuote(primary.Gateway+"%"+primary.Interface))
	case "lte":
		fmt.Fprintf(&code, ":if ([/interface get [find name=%s] running]) do={ :set gw %s };\n", rosQuote(primary.Interface), rosQuote(primary.Interface))
	case "dhcp":
		code.WriteString(":local c [/ip dhcp-client find where interface=" + rosQuote(primary.Interface) + "];\n:if ([:len $c] > 0) do={ :if ([/ip dhcp-client get $c status] = \"bound\") do={\n")
		fmt.Fprintf(&code, ":set gw ([/ip dhcp-client get $c gateway] . %s);\n} };\n", rosQuote("%"+primary.Interface))
	}
	var distanceRead string
	switch primary.Mode {
	case "static":
		distanceRead = "[/ip route get [find comment=mti-wan-0] distance]"
	case "dhcp":
		distanceRead = "[/ip dhcp-client get [find where interface=" + rosQuote(primary.Interface) + "] default-route-distance]"
	case "lte":
		distanceRead = "[/interface lte apn get [find name=mti-wan-0-apn] default-route-distance]"
	}
	fmt.Fprintf(&code, ":if ([:typeof $mtiFailBackup] = \"nothing\") do={ :set mtiFailBackup (%s > 1) };\n", distanceRead)
	code.WriteString(":local route [/ip route find comment=mti-probe-link];\n:if ($gw = \"\") do={ /ip route disable $route } else={\n:if ([:len $route] = 0) do={ /ip route add dst-address=0.0.0.0/0 gateway=$gw routing-table=mti-probe distance=1 comment=mti-probe-link } else={ /ip route set $route gateway=$gw disabled=no }\n};\n")
	fmt.Fprintf(&code, ":local replies 0; :do { :set replies [/ping address=%s count=1 interval=1s] } on-error={ :set replies 0 };\n", rosQuote(target))
	code.WriteString(":if ([:typeof $mtiFailBackup] = \"nothing\") do={ :set mtiFailBackup false };\n:local change false; :local next $mtiFailBackup;\n:if ($replies = 0) do={\n:set mtiFailGood;\n:if ([:typeof $mtiFailBad] = \"nothing\") do={ :set mtiFailBad $now };\n:if (($now - $mtiFailBad >= 30s) && (!$mtiFailBackup)) do={ :set next true; :set change true };\n} else={\n:set mtiFailBad;\n:if ([:typeof $mtiFailGood] = \"nothing\") do={ :set mtiFailGood $now };\n:if (($now - $mtiFailGood >= 1m) && $mtiFailBackup) do={ :set next false; :set change true };\n};\n:if ($change) do={\n:local distance 1; :if ($next) do={ :set distance 3 };\n")
	switch primary.Mode {
	case "static":
		code.WriteString("/ip route set [find comment=mti-wan-0] distance=$distance;\n")
	case "dhcp":
		code.WriteString("/ip dhcp-client set [find where interface=" + rosQuote(primary.Interface) + "] default-route-distance=$distance;\n")
	case "lte":
		code.WriteString("/interface lte apn set [find name=mti-wan-0-apn] default-route-distance=$distance;\n")
	}
	code.WriteString(":set mtiFailBackup $next;\n:log warning (\"mti-failover: backup=\" . $mtiFailBackup);\n};\n")
	b.WriteString(":global mtiFailBad; :set mtiFailBad; :global mtiFailGood; :set mtiFailGood; :global mtiFailBackup; :set mtiFailBackup false;\n")
	fmt.Fprintf(b, "/system script add name=mti-failover policy=read,write,test source=%s\n", rosQuote(code.String()))
	b.WriteString("/system scheduler add name=mti-failover interval=5s start-time=startup on-event=mti-failover policy=read,write,test\n")
}
