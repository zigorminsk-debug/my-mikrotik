package mikrotik

import (
	"crypto/sha256"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// GeoNATSettings restricts one existing dst-nat resource in the forward chain.
type GeoNATSettings struct {
	Enabled         bool     `json:"enabled"`
	Resource        string   `json:"resource"`
	WAN             string   `json:"wan"`
	Destination     string   `json:"destination"`
	Port            string   `json:"port"`
	Protocol        string   `json:"protocol"`
	Countries       []string `json:"countries"`
	CountryPrefixes []string `json:"countryPrefixes"`
	Manual          string   `json:"manual"`
	Acknowledged    bool     `json:"acknowledged"`
}

const MaxGeoPrefixes = 60000

var geoResourceRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)
var countryCodeRE = regexp.MustCompile(`^[a-z]{2}$`)

func natAddresses(s GeoNATSettings) ([]string, error) {
	if len(s.CountryPrefixes) > MaxGeoPrefixes {
		return nil, fmt.Errorf("слишком много подсетей стран (максимум %d)", MaxGeoPrefixes)
	}
	if len(s.Countries) > 12 {
		return nil, fmt.Errorf("можно выбрать не более 12 стран")
	}
	seenCountries := map[string]bool{}
	for _, code := range s.Countries {
		if !countryCodeRE.MatchString(code) || seenCountries[code] {
			return nil, fmt.Errorf("некорректный или повторный код страны")
		}
		seenCountries[code] = true
	}
	if len(s.Countries) > 0 && len(s.CountryPrefixes) == 0 {
		return nil, fmt.Errorf("загрузите подсети выбранных стран")
	}
	if len(s.Countries) == 0 && len(s.CountryPrefixes) > 0 {
		return nil, fmt.Errorf("подсети стран переданы без выбранных стран")
	}
	manual := strings.FieldsFunc(s.Manual, func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) })
	if len(manual) > 1000 {
		return nil, fmt.Errorf("максимум 1000 отдельных адресов или подсетей")
	}
	all := make([]string, 0, len(manual)+len(s.CountryPrefixes))
	all = append(all, s.CountryPrefixes...)
	all = append(all, manual...)
	set := make(map[string]bool, len(all))
	result := make([]string, 0, len(all))
	for _, input := range all {
		value := strings.TrimSpace(input)
		if !strings.Contains(value, "/") {
			value += "/32"
		}
		ip, block, err := net.ParseCIDR(value)
		if err != nil || ip.To4() == nil {
			return nil, fmt.Errorf("неверный IPv4-адрес или подсеть: %q", input)
		}
		ones, bits := block.Mask.Size()
		if bits != 32 {
			return nil, fmt.Errorf("ожидается IPv4-подсеть: %q", input)
		}
		if ones == 0 {
			return nil, fmt.Errorf("подсеть 0.0.0.0/0 не допускается")
		}
		canonical := block.String()
		if !set[canonical] {
			set[canonical] = true
			result = append(result, canonical)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("укажите хотя бы одну страну или IPv4-адрес")
	}
	if len(result) > MaxGeoPrefixes {
		return nil, fmt.Errorf("слишком много адресов (максимум %d)", MaxGeoPrefixes)
	}
	sort.Strings(result)
	return result, nil
}

func validateGeoNAT(s GeoNATSettings, ifaces []Iface, settings *Settings) []string {
	var errs []string
	if settings.Backup.Enabled || settings.Security.Enabled || settings.LAN.Enabled || settings.DHCP.Enabled || settings.DNS.Enabled || settings.NTP.Enabled || settings.PPPoE.Enabled || settings.WiFi.Enabled || settings.WGServer.Enabled || settings.WGClient.Enabled || settings.OpenVPN.Enabled || settings.PPTP.Enabled || settings.OVPNCertificates.Enabled {
		errs = append(errs, "Белый список NAT: выключите остальные разделы и применяйте отдельным скриптом")
	}
	if !s.Acknowledged {
		errs = append(errs, "Белый список NAT: подтвердите, что проверили настройки и резервную копию")
	}
	if !geoResourceRE.MatchString(s.Resource) {
		errs = append(errs, "Белый список NAT: идентификатор ресурса — 1–20 латинских букв, цифр, _ или -")
	}
	found := false
	for _, i := range ifaces {
		if i.Name == s.WAN && !i.Disabled {
			found = true
			break
		}
	}
	if !found {
		errs = append(errs, "Белый список NAT: выберите существующий включённый WAN-интерфейс")
	}
	ip := net.ParseIP(s.Destination)
	if ip == nil || ip.To4() == nil || strings.Contains(s.Destination, ":") || ip.IsUnspecified() || ip.IsMulticast() {
		errs = append(errs, "Белый список NAT: укажите IPv4-адрес внутреннего ресурса")
	}
	if !validPort(s.Port) {
		errs = append(errs, "Белый список NAT: порт внутреннего ресурса должен быть от 1 до 65535")
	}
	if s.Protocol != "tcp" && s.Protocol != "udp" {
		errs = append(errs, "Белый список NAT: выберите TCP или UDP")
	}
	if _, err := natAddresses(s); err != nil {
		errs = append(errs, "Белый список NAT: "+err.Error())
	}
	return errs
}

func writeGeoNAT(b *strings.Builder, s GeoNATSettings, now time.Time) {
	addresses, _ := natAddresses(s)
	// The marker is unique to this generated script; repeated imports fail safely.
	sum := sha256.Sum256([]byte(now.UTC().Format(time.RFC3339Nano) + "|" + s.Resource + "|" + strings.Join(addresses, ",")))
	resourceHash := sha256.Sum256([]byte(s.Resource))
	prefix := fmt.Sprintf("mti-nat-%x-", resourceHash[:8])
	tag := fmt.Sprintf("%s%x", prefix, sum[:5])
	list := tag
	section(b, "IP / Firewall", "Белый список для dst-nat: "+s.Resource)
	b.WriteString("# Только IPv4. Резервная копия обязательна; импортируйте локально, а не через API.\n")
	b.WriteString("# Страны основаны на делегировании адресов RIR, а не на достоверном местоположении пользователя.\n")
	if len(s.Countries) > 0 {
		fmt.Fprintf(b, "# Страны: %s; источник ipverse/country-ip-blocks.\n", strings.Join(s.Countries, ","))
	}
	fmt.Fprintf(b, ":if ([:len [/ip firewall address-list find where list=%s]] > 0 || [:len [/ip firewall filter find where comment=%s]] > 0) do={ :error \"this NAT whitelist script was already applied; regenerate\" }\n", rosQuote(tag), rosQuote(tag+"-drop"))
	for _, addr := range addresses {
		fmt.Fprintf(b, "/ip firewall address-list add %s %s %s\n", attr("list", list), attr("address", addr), attr("comment", tag))
	}
	match := strings.Join([]string{"chain=forward", attr("in-interface", s.WAN), "connection-nat-state=dstnat", attr("dst-address", s.Destination), attr("protocol", s.Protocol), attr("dst-port", s.Port)}, " ")
	// Insert accept first and then drop, both ahead of existing accept/fasttrack rules.
	fmt.Fprintf(b, "/ip firewall filter add %s %s action=accept %s\n", match, attr("src-address-list", list), attr("comment", tag+"-allow"))
	fmt.Fprintf(b, "/ip firewall filter move [find where comment=%s] destination=0\n", rosQuote(tag+"-allow"))
	fmt.Fprintf(b, "/ip firewall filter add %s src-address-list=!%s action=drop %s\n", match, list, attr("comment", tag+"-drop"))
	fmt.Fprintf(b, "/ip firewall filter move [find where comment=%s] destination=0\n", rosQuote(tag+"-drop"))
	// Remove only older objects owned by this resource after new rules are live.
	fmt.Fprintf(b, "/ip firewall filter remove [find where comment~%s and comment!=%s and comment!=%s]\n", rosQuote("^"+prefix), rosQuote(tag+"-allow"), rosQuote(tag+"-drop"))
	fmt.Fprintf(b, "/ip firewall address-list remove [find where comment~%s and comment!=%s]\n", rosQuote("^"+prefix), rosQuote(tag))
	b.WriteString("# FastTrack и уже установленные соединения могут продолжать работать до истечения conntrack. Проверьте правила и доступ извне.\n")
}
