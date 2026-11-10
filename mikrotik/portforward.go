package mikrotik

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// PortForwardSettings manages only rules tagged mti-forward-*; existing rules are preserved.
type PortForwardSettings struct {
	Enabled bool              `json:"enabled"`
	Rules   []PortForwardRule `json:"rules"`
}

type PortForwardRule struct {
	WAN          string `json:"wan"`
	Protocol     string `json:"protocol"`
	ExternalPort string `json:"externalPort"`
	InternalIP   string `json:"internalIP"`
	InternalPort string `json:"internalPort"`
	PublicIP     string `json:"publicIP"`
	Source       string `json:"source"`
	Hairpin      bool   `json:"hairpin"`
	LAN          string `json:"lan"`
}

func portNumber(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && n <= 65535
}

func validatePortForward(s PortForwardSettings) []string {
	var errs []string
	if len(s.Rules) == 0 || len(s.Rules) > 64 {
		return []string{"Проброс портов: необходимо от 1 до 64 правил"}
	}
	seen := map[string]bool{}
	for i, r := range s.Rules {
		var problems []string
		if strings.TrimSpace(r.WAN) == "" {
			problems = append(problems, "укажите WAN-интерфейс")
		}
		if r.Protocol != "tcp" && r.Protocol != "udp" {
			problems = append(problems, "протокол должен быть tcp или udp")
		}
		if !portNumber(r.ExternalPort) || !portNumber(r.InternalPort) {
			problems = append(problems, "порты должны быть числами 1–65535")
		}
		if !validIPv4(r.InternalIP) {
			problems = append(problems, "некорректный локальный IPv4")
		}
		if r.PublicIP != "" && !validIPv4(r.PublicIP) {
			problems = append(problems, "некорректный внешний IPv4")
		}
		if r.Source != "" {
			ip, _, err := net.ParseCIDR(r.Source)
			if !validIPv4(r.Source) && (err != nil || ip.To4() == nil) {
				problems = append(problems, "источник должен быть IPv4 или IPv4/CIDR")
			}
		}
		if r.Hairpin {
			if r.Source != "" {
				problems = append(problems, "hairpin с ограничением источника пока не поддерживается; используйте отдельное правило")
			}
			ip, network, err := net.ParseCIDR(r.LAN)
			if r.PublicIP == "" {
				problems = append(problems, "для hairpin укажите внешний IPv4")
			}
			if err != nil || ip.To4() == nil {
				problems = append(problems, "для hairpin укажите LAN IPv4/CIDR")
			} else if !network.Contains(net.ParseIP(r.InternalIP)) {
				problems = append(problems, "локальный сервер должен находиться в LAN hairpin")
			}
		}
		key := r.WAN + "/" + r.Protocol + "/" + r.ExternalPort
		if seen[key] {
			problems = append(problems, "повтор WAN/протокол/внешний порт")
		}
		seen[key] = true
		if len(problems) > 0 {
			errs = append(errs, fmt.Sprintf("Проброс портов, правило %d: %s", i+1, strings.Join(problems, ", ")))
		}
	}
	return errs
}

func buildPortForward(b *strings.Builder, s PortForwardSettings) {
	section(b, "IP / Firewall", "Проброс портов")
	b.WriteString("# Проверьте порядок NAT и разрешение connection-nat-state=dstnat в forward.\n# Правила фильтрации не изменяются автоматически.\n")
	// Exact ownership tags, not a prefix match, to avoid removing unrelated rules.
	for i := 0; i < 64; i++ {
		for _, suffix := range []string{"wan", "loop", "src"} {
			fmt.Fprintf(b, "/ip firewall nat remove [find comment=%s]\n", rosQuote(fmt.Sprintf("mti-forward-%d-%s", i, suffix)))
		}
	}
	for i, r := range s.Rules {
		base := "chain=dstnat " + attr("protocol", r.Protocol) + " " + attr("dst-port", r.ExternalPort)
		if r.PublicIP != "" {
			base += " " + attr("dst-address", r.PublicIP)
		}
		if r.Source != "" {
			base += " " + attr("src-address", r.Source)
		}
		target := " action=dst-nat " + attr("to-addresses", r.InternalIP) + " " + attr("to-ports", r.InternalPort)
		fmt.Fprintf(b, "/ip firewall nat add %s %s%s %s\n", base, attr("in-interface", r.WAN), target, attr("comment", fmt.Sprintf("mti-forward-%d-wan", i)))
		if r.Hairpin {
			// Source restriction, when present, must also apply to loopback traffic.
			loop := "chain=dstnat " + attr("protocol", r.Protocol) + " " + attr("dst-port", r.ExternalPort) + " " + attr("dst-address", r.PublicIP) + " " + attr("src-address", r.LAN)
			fmt.Fprintf(b, "/ip firewall nat add %s%s %s\n", loop, target, attr("comment", fmt.Sprintf("mti-forward-%d-loop", i)))
			fmt.Fprintf(b, "/ip firewall nat add chain=srcnat %s %s %s %s connection-nat-state=dstnat action=masquerade %s\n", attr("src-address", r.LAN), attr("dst-address", r.InternalIP), attr("protocol", r.Protocol), attr("dst-port", r.InternalPort), attr("comment", fmt.Sprintf("mti-forward-%d-src", i)))
		}
	}
}
