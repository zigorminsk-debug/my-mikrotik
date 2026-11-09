package mikrotik

import (
	"fmt"
	"net"
	"strings"
)

// WANSettings supports up to two independent IPv4 uplinks.
// DHCP uses lease-provided gateways; static links may check their gateway by ping.
type WANSettings struct {
	Enabled bool      `json:"enabled"`
	Links   []WANLink `json:"links"`
}

type WANLink struct {
	Interface    string `json:"iface"`
	Mode         string `json:"mode"`
	Address      string `json:"address"`
	Gateway      string `json:"gateway"`
	Distance     int    `json:"distance"`
	NAT          bool   `json:"nat"`
	CheckGateway bool   `json:"checkGateway"`
}

func validateWAN(s WANSettings, pppoe PPPoESettings) []string {
	var errs []string
	if len(s.Links) < 1 || len(s.Links) > 2 {
		return []string{"WAN: необходимо один или два канала"}
	}
	interfaces := map[string]bool{}
	distances := map[int]bool{}
	for i, r := range s.Links {
		var problems []string
		if strings.TrimSpace(r.Interface) == "" {
			problems = append(problems, "выберите интерфейс")
		}
		if interfaces[r.Interface] {
			problems = append(problems, "интерфейсы каналов должны различаться")
		}
		interfaces[r.Interface] = true
		if pppoe.Enabled && pppoe.Interface == r.Interface {
			problems = append(problems, "интерфейс уже выбран для PPPoE; совместное использование в этом режиме не поддерживается")
		}
		if r.Distance < 1 || r.Distance > 254 {
			problems = append(problems, "приоритет должен быть 1–254")
		}
		if distances[r.Distance] {
			problems = append(problems, "для резервирования нужны разные приоритеты")
		}
		distances[r.Distance] = true
		switch r.Mode {
		case "dhcp":
			if r.CheckGateway {
				problems = append(problems, "ping шлюза в режиме DHCP пока не поддерживается")
			}
		case "static":
			ip, network, err := net.ParseCIDR(r.Address)
			gateway := net.ParseIP(r.Gateway)
			if err != nil || ip.To4() == nil {
				problems = append(problems, "укажите WAN IPv4/CIDR")
			} else if !usableWANHost(ip, network) {
				problems = append(problems, "WAN-адрес не должен быть адресом сети или broadcast")
			} else if gateway == nil || gateway.To4() == nil || !network.Contains(gateway) || gateway.Equal(ip) || !usableWANHost(gateway, network) {
				problems = append(problems, "шлюз должен быть отдельным IPv4 в подсети WAN")
			}
		default:
			problems = append(problems, "способ подключения должен быть dhcp или static")
		}
		if len(problems) > 0 {
			errs = append(errs, fmt.Sprintf("WAN, канал %d: %s", i+1, strings.Join(problems, ", ")))
		}
	}
	if pppoe.Enabled && pppoe.DefaultRoute {
		errs = append(errs, "WAN: отключите маршрут по умолчанию в отдельном разделе PPPoE, чтобы избежать конфликтующих приоритетов")
	}
	return errs
}

func buildWAN(b *strings.Builder, s WANSettings) {
	section(b, "IP / WAN", "Основной и резервный IPv4-каналы")
	b.WriteString("# Сохраните export/backup. Проверьте существующие DHCP clients, адреса и default routes.\n# Меньшая distance — основной канал. DHCP переключается при потере lease/link.\n# check-gateway=ping проверяет только шлюз, НЕ интернет за ним.\n")
	for i := 0; i < 2; i++ {
		tag := fmt.Sprintf("mti-wan-%d", i)
		for _, path := range []string{"/ip dhcp-client", "/ip route", "/ip address", "/ip firewall nat"} {
			fmt.Fprintf(b, "%s remove [find comment=%s]\n", path, rosQuote(tag))
		}
	}
	for i, r := range s.Links {
		tag := attr("comment", fmt.Sprintf("mti-wan-%d", i))
		if r.Mode == "dhcp" {
			fmt.Fprintf(b, "/ip dhcp-client add %s add-default-route=yes default-route-distance=%d use-peer-dns=no disabled=no %s\n", attr("interface", r.Interface), r.Distance, tag)
		} else {
			fmt.Fprintf(b, "/ip address add %s %s %s\n", attr("interface", r.Interface), attr("address", r.Address), tag)
			check := "none"
			if r.CheckGateway {
				check = "ping"
			}
			// Interface-qualified gateway avoids ambiguity when providers use identical private gateways.
			fmt.Fprintf(b, "/ip route add dst-address=0.0.0.0/0 %s distance=%d check-gateway=%s %s\n", attr("gateway", r.Gateway+"%"+r.Interface), r.Distance, check, tag)
		}
		if r.NAT {
			fmt.Fprintf(b, "/ip firewall nat add chain=srcnat %s action=masquerade %s\n", attr("out-interface", r.Interface), tag)
		}
	}
}

func validateWANLocal(s WANSettings, lan LanSettings) []string {
	if !lan.Enabled {
		return nil
	}
	var errs []string
	_, local, err := net.ParseCIDR(lan.Address)
	for i, r := range s.Links {
		if r.Interface == lan.Interface {
			errs = append(errs, fmt.Sprintf("WAN, канал %d: интерфейс совпадает с LAN", i+1))
		}
		if r.Mode == "static" && err == nil {
			_, remote, e := net.ParseCIDR(r.Address)
			if e == nil && (local.Contains(remote.IP) || remote.Contains(local.IP)) {
				errs = append(errs, fmt.Sprintf("WAN, канал %d: подсеть пересекается с LAN", i+1))
			}
		}
	}
	return errs
}

// /31 point-to-point links have two usable endpoints; /32 cannot have a separate on-link gateway.
func usableWANHost(ip net.IP, network *net.IPNet) bool {
	ones, bits := network.Mask.Size()
	if bits != 32 || ones == 32 {
		return false
	}
	if ones == 31 {
		return true
	}
	v := ip.To4()
	if v == nil {
		return false
	}
	broadcast := append(net.IP(nil), network.IP.To4()...)
	for i := range broadcast {
		broadcast[i] |= ^network.Mask[i]
	}
	return !ip.Equal(network.IP) && !ip.Equal(broadcast)
}
