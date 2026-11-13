package mikrotik

import (
	"fmt"
	"strings"
)

// buildUSBFailover is deliberately separate from the legacy controller. It
// neither changes the APN nor edits the dynamically created DHCP client. The
// owned main-table default follows the currently selected DHCP gateway; the
// probe default stays on the phone. It is experimental until verified on ROS.
func buildUSBFailover(b *strings.Builder, s WANSettings) {
	if !s.Failover || len(s.Links) != 2 {
		return // Validation should already have rejected this configuration.
	}
	primary, backup := s.Links[0], s.Links[1]
	target := s.ProbeHost
	if target == "" {
		target = "8.8.8.8"
	}
	b.WriteString("# MTI-USB-PREVIEW-ONLY\n")
	b.WriteString("# ЭКСПЕРИМЕНТАЛЬНО: USB tethering; перед применением backup, export и локальный доступ.\n")
	b.WriteString("# Маршрут mti-wan-0 направляется на шлюз выбранного DHCP-канала; чужие маршруты и APN не меняются.\n")
	b.WriteString(":if ([:len [/routing table find name=mti-probe]] = 0) do={ /routing table add name=mti-probe fib }\n")
	b.WriteString("/ip route add blackhole dst-address=0.0.0.0/0 routing-table=mti-probe distance=254 comment=mti-probe\n")
	fmt.Fprintf(b, "/ip firewall mangle add chain=output protocol=icmp %s action=mark-routing new-routing-mark=mti-probe passthrough=no comment=mti-probe\n", attr("dst-address", target))
	b.WriteString(":do { /ip firewall mangle move [find comment=mti-probe] 0 } on-error={ :log warning \"mti-usb: правило пробы осталось на прежнем месте\" }\n")

	var code strings.Builder
	code.WriteString(`:if ([/system script job print count-only as-value where script=[:jobname]] > 1) do={ :error "mti-usb already running" };
:global mtiUsbBackup; :global mtiUsbBad; :global mtiUsbGood; :global mtiUsbLast;
:local now [/system resource get uptime];
:if ([:typeof $mtiUsbBackup] = "nothing") do={ :set mtiUsbBackup false };
:local primaryGw ""; :local backupGw "";
`)
	// Never guess an interface-only gateway for USB tethering. A missing,
	// unbound, ambiguous or unreadable lease leaves that gateway unavailable.
	fmt.Fprintf(&code, `:do { :local c [/ip dhcp-client find where interface=%s]; :if ([:len $c] = 1) do={ :set c [:pick $c 0]; :if ([/ip dhcp-client get $c status] = "bound") do={ :local g [/ip dhcp-client get $c gateway]; :if ([:len $g] > 0) do={ :set primaryGw ($g . %s) } } } } on-error={ :set primaryGw "" };
`, rosQuote(primary.Interface), rosQuote("%"+primary.Interface))
	fmt.Fprintf(&code, `:do { :local c [/ip dhcp-client find where interface=%s]; :if ([:len $c] = 1) do={ :set c [:pick $c 0]; :if ([/ip dhcp-client get $c status] = "bound") do={ :local g [/ip dhcp-client get $c gateway]; :if ([:len $g] > 0) do={ :set backupGw ($g . %s) } } } } on-error={ :set backupGw "" };
`, rosQuote(backup.Interface), rosQuote("%"+backup.Interface))
	code.WriteString(`:local replies 0; :local probeOK false; :local switched false;
:if ($primaryGw = "") do={
  :do { /ip route disable [find where comment=mti-probe-link and disabled=no] } on-error={};
  :set mtiUsbBad; :set mtiUsbGood;
  :if (!$mtiUsbBackup) do={ :set mtiUsbBackup true; :set switched true; :log warning "mti-usb: шлюз телефона недоступен, выбран резерв" };
} else={
  :do {
    :local pr [/ip route find where comment=mti-probe-link];
    :if ([:len $pr] = 0) do={ /ip route add dst-address=0.0.0.0/0 gateway=$primaryGw routing-table=mti-probe distance=1 comment=mti-probe-link; :set probeOK true } else={
      :if ([:len $pr] = 1) do={ :set pr [:pick $pr 0];
        :if (([:tostr [/ip route get $pr gateway]] != $primaryGw) || [/ip route get $pr disabled]) do={ /ip route set $pr gateway=$primaryGw disabled=no };
        :set probeOK true;
      };
    };
  } on-error={ :set probeOK false };
`)
	fmt.Fprintf(&code, ":if ($probeOK) do={ :do { :local r [/ping address=%s count=3 interval=1s]; :if ([:typeof $r] = \"num\") do={ :set replies $r } } on-error={ :set replies 0 } };\n", rosQuote(target))
	code.WriteString(`:if (!$probeOK) do={ :log warning "mti-usb: маршрут пробы не создан; проверка недостоверна" } else={
  :if ($replies = 0) do={
    :set mtiUsbGood; :if ([:typeof $mtiUsbBad] = "nothing") do={ :set mtiUsbBad $now };
    :if ((!$mtiUsbBackup) && (($now - $mtiUsbBad) >= 30s)) do={ :set mtiUsbBackup true; :set switched true; :log warning "mti-usb: выбран резерв после 30 секунд без ответа" };
  } else={
    :set mtiUsbBad; :if ([:typeof $mtiUsbGood] = "nothing") do={ :set mtiUsbGood $now };
    :if ($mtiUsbBackup && (($now - $mtiUsbGood) >= 1m)) do={ :set mtiUsbBackup false; :set switched true; :log warning "mti-usb: выбран основной после 60 секунд ответов" };
  };
};
};
:local selected $primaryGw; :if ($mtiUsbBackup) do={ :set selected $backupGw };
:local managed [/ip route find where comment=mti-wan-0];
:local conflict false; :local applied false; :local previous "";
:if ([:len $managed] > 1) do={ :set conflict true; :log warning "mti-usb: найдено несколько mti-wan-0; маршрут не меняется" };
:if ([:len $managed] = 1) do={ :do { :set previous [:tostr [/ip route get [:pick $managed 0] gateway]] } on-error={} };
# Never override an existing competing default at distance 0 or 1.
:do {
  :foreach r in=[/ip route find where dst-address=0.0.0.0/0 and routing-table=main] do={
    :local own false; :if ([:len $managed] = 1) do={ :if ($r = [:pick $managed 0]) do={ :set own true } };
    :if (!$own) do={ :if ((![/ip route get $r disabled]) && ([/ip route get $r distance] <= 1)) do={ :set conflict true } };
  };
} on-error={ :set conflict true };
:if (($selected = "") || $conflict) do={
  :if ([:len $managed] = 1) do={ :do { /ip route disable [:pick $managed 0] } on-error={} };
  :if ($conflict) do={ :log warning "mti-usb: конфликт приоритетов или дубликаты; управляемый маршрут не включен" } else={ :log warning "mti-usb: шлюз выбранного канала отсутствует; управляемый маршрут отключен" };
} else={
  :do {
    :if ([:len $managed] = 0) do={
      /ip route add dst-address=0.0.0.0/0 gateway=$selected distance=1 comment=mti-wan-0;
    } else={
      :set managed [:pick $managed 0];
      :if (([:tostr [/ip route get $managed gateway]] != $selected) || ([/ip route get $managed distance] != 1) || [/ip route get $managed disabled]) do={ /ip route set $managed gateway=$selected distance=1 disabled=no };
    };
    :set applied true;
  } on-error={ :log warning "mti-usb: ошибка установки управляемого маршрута" };
};
# Existing NAT sessions can retain the old WAN; clearing them is disruptive.
:if ($applied && ($switched || (($previous != "") && ($previous != $selected)))) do={
  :do { /ip firewall connection remove [find] } on-error={ :log warning "mti-usb: не удалось очистить существующие соединения" };
};
:if (([:typeof $mtiUsbLast] = "nothing") || (($now - $mtiUsbLast) >= 30s)) do={
  :set mtiUsbLast $now; :log info ("mti-usb: backup=" . $mtiUsbBackup . ", проба=" . $replies . "/3, основной=" . $primaryGw . ", резерв=" . $backupGw . ", выбран=" . $selected . ", применён=" . $applied);
};
`)
	b.WriteString(":global mtiUsbBackup; :set mtiUsbBackup false; :global mtiUsbBad; :set mtiUsbBad; :global mtiUsbGood; :set mtiUsbGood; :global mtiUsbLast; :set mtiUsbLast;\n")
	fmt.Fprintf(b, "/system script add name=mti-failover policy=read,write,policy,test source=%s\n", rosQuote(code.String()))
	b.WriteString("/system scheduler add name=mti-failover interval=5s start-time=startup on-event=mti-failover policy=read,write,policy,test\n")
}
