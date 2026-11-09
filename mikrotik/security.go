package mikrotik

import (
	"fmt"
	"net"
	"strings"
)

// SecuritySettings controls an explicitly scoped WAN input firewall policy.
type SecuritySettings struct {
	Enabled      bool     `json:"enabled"`
	WAN          []string `json:"wan"`
	Trusted      string   `json:"trusted"`
	WinboxPort   string   `json:"winboxPort"`
	Acknowledged bool     `json:"acknowledged"`
}

func validateSecurity(s SecuritySettings, ifaces []Iface, settings *Settings) []string {
	var errs []string
	if settings.GeoNAT.Enabled || settings.LAN.Enabled || settings.DHCP.Enabled || settings.DNS.Enabled || settings.NTP.Enabled || settings.PPPoE.Enabled || settings.WiFi.Enabled || settings.WGServer.Enabled || settings.WGClient.Enabled || settings.OpenVPN.Enabled || settings.PPTP.Enabled || settings.OVPNCertificates.Enabled {
		errs = append(errs, "Защита: выключите остальные разделы и применяйте защиту отдельным скриптом")
	}
	if !s.Acknowledged {
		errs = append(errs, "Защита: подтвердите резервную копию и доступ через WinBox/консоль")
	}
	if len(s.WAN) > 3 {
		errs = append(errs, "Защита: допускается не более трёх WAN-интерфейсов для безопасного применения")
	}
	if len(s.WAN) == 0 {
		errs = append(errs, "Защита: выберите все внешние интерфейсы WAN")
	}
	seen := map[string]bool{}
	for _, name := range s.WAN {
		found := false
		for _, i := range ifaces {
			if i.Name == name && !i.Disabled {
				found = true
				break
			}
		}
		if settings.PPPoE.Enabled && settings.PPPoE.Name == name {
			found = true
		}
		if !found || seen[name] {
			errs = append(errs, "Защита: неизвестный, выключенный или повторный WAN-интерфейс: "+name)
		}
		seen[name] = true
	}
	if !validPort(s.WinboxPort) {
		errs = append(errs, "Защита: порт WinBox должен быть от 1 до 65535")
	}
	entries := splitList(s.Trusted)
	if len(entries) > 4 {
		errs = append(errs, "Защита: укажите не более четырёх доверенных адресов или подсетей")
	}
	if len(entries) == 0 {
		errs = append(errs, "Защита: укажите доверенные IPv4-адреса или подсети для WinBox")
	}
	for _, entry := range entries {
		ip := net.ParseIP(entry)
		if ip != nil && ip.To4() != nil {
			continue
		}
		ip, cidr, err := net.ParseCIDR(entry)
		if err != nil || ip.To4() == nil {
			errs = append(errs, "Защита: некорректный доверенный IPv4-адрес или CIDR: "+entry)
			continue
		}
		ones, _ := cidr.Mask.Size()
		if ones == 0 {
			errs = append(errs, "Защита: 0.0.0.0/0 нельзя использовать как доверенную сеть")
		}
	}
	return errs
}

func writeSecurity(b *strings.Builder, s SecuritySettings, ipv6 bool) {
	section(b, "IP / System", "Защита WAN и доступ только через WinBox")
	b.WriteString("# Выполняйте в WinBox Safe Mode; сохраните резервную копию и убедитесь, что ваш IP разрешён.\n")
	b.WriteString("# Все подключения к VPN-серверам на выбранных WAN будут блокироваться; настройки сервисов действуют на всех интерфейсах.\n")
	b.WriteString(":if ([:len [/ip firewall filter find where comment~\"^mti-sec-\"]] > 0) do={ :error \"mti security rules already exist; stop\" }\n")
	if ipv6 {
		b.WriteString(":if ([:len [/ipv6 firewall filter find where comment~\"^mti-sec-\"]] > 0) do={ :error \"mti IPv6 security rules already exist; stop\" }\n")
	}
	// Install exceptions before drops, so an active management session is not interrupted mid-script.
	rule := func(menu, args, comment string) {
		fmt.Fprintf(b, "%s add chain=input %s comment=%s\n", menu, args, attr("comment", comment))
		fmt.Fprintf(b, "%s move [find where comment=%s] destination=0\n", menu, attr("comment", comment))
	}
	drop := func(menu, args, comment string) {
		fmt.Fprintf(b, "%s add chain=input %s action=drop comment=%s\n", menu, args, attr("comment", comment))
		fmt.Fprintf(b, "{ :local first [%s find where !(comment~\"^mti-sec-\")]; :if ([:len $first] > 0) do={ %s move [find where comment=%s] destination=[:pick $first 0] } }\n", menu, menu, attr("comment", comment))
	}
	for n, iface := range s.WAN {
		prefix := fmt.Sprintf("mti-sec-%d-", n)
		in := attr("in-interface", iface)
		rule("/ip firewall filter", in+" protocol=udp src-port=67 dst-port=68 action=accept", prefix+"dhcp4")
		for j, addr := range splitList(s.Trusted) {
			rule("/ip firewall filter", in+" protocol=tcp "+attr("dst-port", s.WinboxPort)+" "+attr("src-address", addr)+" action=accept", fmt.Sprintf("%swinbox-%d", prefix, j))
		}
		rule("/ip firewall filter", in+" connection-state=established,related action=accept", prefix+"est4")
		drop("/ip firewall filter", in, prefix+"drop4")
		if ipv6 {
			rule("/ipv6 firewall filter", in+" protocol=icmpv6 action=accept", prefix+"icmp6")
			rule("/ipv6 firewall filter", in+" protocol=udp src-port=547 dst-port=546 action=accept", prefix+"dhcp6")
			rule("/ipv6 firewall filter", in+" connection-state=established,related action=accept", prefix+"est6")
			drop("/ipv6 firewall filter", in, prefix+"drop6")
		}
	}
	b.WriteString("/ip service set [find where name=winbox] disabled=no " + attr("port", s.WinboxPort) + " " + attr("address", strings.Join(splitList(s.Trusted), ",")) + "\n")
	b.WriteString("/tool mac-server set allowed-interface-list=none\n/tool mac-server mac-winbox set allowed-interface-list=none\n/ip neighbor discovery-settings set discover-interface-list=none\n")
	b.WriteString("/tool bandwidth-server set enabled=no\n/ip proxy set enabled=no\n/ip socks set enabled=no\n/ip upnp set enabled=no\n")
	for _, service := range []string{"telnet", "ftp", "www", "www-ssl", "ssh", "api-ssl", "api"} {
		fmt.Fprintf(b, "/ip service disable [find where name=%s]\n", service)
	}
	b.WriteString("# Управление через API/REST отключено. Для повторного подключения приложения их потребуется вручную включить.\n")
}
