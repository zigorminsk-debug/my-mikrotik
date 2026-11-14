package mikrotik

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// GeoApplyRequest binds the checked script to the exact preview inputs. The
// preview and checks are not an authentication boundary or router transaction.
type GeoApplyRequest struct {
	Conn        Params         `json:"conn"`
	Device      *Info          `json:"device"`
	Interfaces  []Iface        `json:"interfaces"`
	Geo         GeoNATSettings `json:"geoNat"`
	GeneratedAt time.Time      `json:"generatedAt"`
	Script      string         `json:"script"`
}

// GeoPreflightError guarantees that no configuration write was attempted by this request.
type GeoPreflightError struct{ Cause error }

func (e *GeoPreflightError) Error() string { return e.Cause.Error() }
func (e *GeoPreflightError) Unwrap() error { return e.Cause }

// ApplyGeoNAT accepts only an exact GeoNAT-only preview and performs read-only
// preflight on the chosen transport before any writes. It never falls back or
// retries after an attempt; a failed request may have partially modified RouterOS.
func ApplyGeoNAT(ctx context.Context, req GeoApplyRequest) (protocol string, err error) {
	attempted := false
	defer func() {
		if err != nil && !attempted {
			err = &GeoPreflightError{Cause: err}
		}
	}()
	p := req.Conn
	if err := p.normalize(); err != nil {
		return "", err
	}
	if p.Protocol != "api" && p.Protocol != "rest" {
		return "", fmt.Errorf("автоматическое применение белого списка доступно только по API или REST; MAC-Telnet не поддерживается")
	}
	if req.Device == nil || req.Device.Protocol != p.Protocol || req.Device.Identity == "" || req.Device.RouterOS == "" || req.Device.Architecture == "" {
		return "", fmt.Errorf("нет данных последнего подключения; подключитесь заново")
	}
	if strings.ContainsAny(req.Device.Model+req.Device.RouterOS, "\r\n\x00") {
		return "", fmt.Errorf("некорректные данные устройства; подключитесь заново")
	}
	if !strings.HasPrefix(req.Device.RouterOS, "7.") {
		return "", fmt.Errorf("автоматическое применение белого списка поддерживается только на RouterOS 7")
	}
	if !req.Geo.Enabled || !req.Geo.Acknowledged {
		return "", fmt.Errorf("подтвердите проверку ресурса и резервной копии")
	}
	if req.GeneratedAt.IsZero() {
		return "", fmt.Errorf("нет данных предпросмотра; создайте его заново")
	}
	if len(req.Script) == 0 || len(req.Script) > maxScriptSize {
		return "", fmt.Errorf("автоматическое применение ограничено %d байтами; создайте меньший список или используйте локальный импорт после проверки", maxScriptSize)
	}
	settings := Settings{GeoNAT: req.Geo}
	generated := Generate(req.Device, req.Interfaces, &settings, req.GeneratedAt)
	if !generated.OK {
		return "", fmt.Errorf("проверка настроек: %s", generated.Error)
	}
	if generated.Script != req.Script || !strings.Contains(generated.Script, "# MTI-GEONAT-GUARDED-APPLY") {
		return "", fmt.Errorf("предпросмотр изменился; создайте его заново")
	}

	var read snapshotReader
	var run func() error
	if p.Protocol == "api" {
		c, err := dialAPI(ctx, &p)
		if err != nil {
			return "", fmt.Errorf("не удалось подключиться для проверки: %w", err)
		}
		defer c.Close()
		if err = c.login(p.User, p.Password); err != nil {
			return "", fmt.Errorf("не удалось авторизоваться для проверки")
		}
		c.opTimeout = 90 * time.Second
		read = func(path string) ([]map[string]string, error) {
			recs, _, err := c.exec(path + "/print")
			return recs, err
		}
		run = func() error { return applyAPIClient(c, req.Script) }
	} else {
		rc, err := newRESTClient(&p)
		if err != nil {
			return "", err
		}
		rc.client.Timeout = 90 * time.Second
		read = func(path string) ([]map[string]string, error) {
			recs, err := rc.get(ctx, "/rest"+path)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]string, 0, len(recs))
			for _, rec := range recs {
				m := make(map[string]string, len(rec))
				for k := range rec {
					m[k] = jstr(rec, k)
				}
				out = append(out, m)
			}
			return out, nil
		}
		run = func() error { return applyRESTClient(ctx, rc, req.Script) }
	}
	if err := checkGeoDevice(req.Device, read); err != nil {
		return "", err
	}
	if err := checkGeoNAT(req.Geo, read); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("проверка прервана до применения")
	}
	attempted = true
	if err := run(); err != nil {
		return "", fmt.Errorf("применение не завершилось: настройки могли измениться частично; не повторяйте без проверки: %w", err)
	}
	return p.Protocol, nil
}

func checkGeoDevice(expected *Info, read snapshotReader) error {
	identity, err := read("/system/identity")
	if err != nil || len(identity) != 1 {
		return fmt.Errorf("не удалось повторно проверить identity; настройки не изменены")
	}
	name := identity[0]["name"]
	if name == "" {
		name = identity[0]["identity"]
	}
	resource, err := read("/system/resource")
	if err != nil || len(resource) != 1 {
		return fmt.Errorf("не удалось повторно проверить устройство; настройки не изменены")
	}
	r := resource[0]
	if name != expected.Identity || r["version"] != expected.RouterOS || r["architecture-name"] != expected.Architecture || (expected.Diagnostics != nil && expected.Diagnostics.ResourceBoard != "" && r["board-name"] != expected.Diagnostics.ResourceBoard) {
		return fmt.Errorf("данные устройства изменились; подключитесь заново")
	}
	return nil
}

// Unknown expressions are treated as potentially overlapping, not as safe.
func geoIPMayOverlap(value, destination string) bool {
	if value == "" {
		return true
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.Equal(net.ParseIP(destination))
	}
	if _, network, err := net.ParseCIDR(value); err == nil {
		return network.Contains(net.ParseIP(destination))
	}
	return true
}
func geoPortMayOverlap(value, port string) bool {
	if value == "" {
		return true
	}
	if n, err := strconv.Atoi(value); err == nil && n > 0 && n <= 65535 {
		wanted, err := strconv.Atoi(port)
		return err != nil || wanted < 1 || wanted > 65535 || n == wanted
	}
	return true
}

// RouterOS may encode TCP/UDP as protocol names or IANA numbers.
func geoProtocol(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "tcp", "6":
		return "tcp"
	case "udp", "17":
		return "udp"
	}
	return ""
}
func geoProtocolMayOverlap(value, expected string) bool {
	p := geoProtocol(value)
	return p == "" || p == expected
}
func geoFalse(v string) bool { return explicitlyFalse(v) || v == "0" }
func geoTrue(v string) bool  { return explicitlyTrue(v) || v == "1" }
func geoSinglePort(value, expected string) bool {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 1 || n > 65535 {
		return false
	}
	if len(parts) == 2 {
		end, err := strconv.Atoi(parts[1])
		if err != nil || end != n {
			return false
		}
	}
	want, err := strconv.Atoi(expected)
	return err == nil && want == n
}
func geoSingleAddress(value string, target net.IP) bool {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	ip := net.ParseIP(parts[0])
	if ip == nil || ip.To4() == nil || !ip.Equal(target) {
		return false
	}
	if len(parts) == 2 {
		other := net.ParseIP(parts[1])
		return other != nil && other.Equal(ip)
	}
	return true
}

func explicitlyFalse(v string) bool { return v == "false" || v == "no" }
func explicitlyTrue(v string) bool  { return v == "true" || v == "yes" }

// checkGeoNAT is intentionally conservative: only exact single-address, single-
// port dst-nat to a non-local target is accepted. It checks existence, not rule
// ordering, public reachability, RDP, firewall behavior or endpoint identity.
func checkGeoNAT(s GeoNATSettings, read snapshotReader) error {
	ifs, err := read("/interface")
	if err != nil {
		return fmt.Errorf("не удалось проверить WAN-интерфейс; настройки не изменены")
	}
	matches := 0
	for _, i := range ifs {
		if i["name"] == s.WAN && geoFalse(i["disabled"]) {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("WAN-интерфейс отсутствует, выключен или не удалось подтвердить его состояние; настройки не изменены")
	}
	addresses, err := read("/ip/address")
	if err != nil || len(addresses) == 0 {
		return fmt.Errorf("не удалось проверить адреса роутера; настройки не изменены")
	}
	dest := net.ParseIP(s.Destination).To4()
	if dest == nil {
		return fmt.Errorf("некорректный адрес ресурса")
	}
	for _, rec := range addresses {
		host, _, e := net.ParseCIDR(rec["address"])
		if e != nil || host.To4() == nil {
			return fmt.Errorf("не удалось однозначно проверить адреса роутера; настройки не изменены")
		}
		if host.Equal(dest) {
			return fmt.Errorf("указан адрес самого роутера; этот список работает в forward и не защищает сервисы input")
		}
	}
	filters, err := read("/ip/firewall/filter")
	if err != nil {
		return fmt.Errorf("не удалось проверить служебные правила firewall; настройки не изменены")
	}
	resourceHash := sha256.Sum256([]byte(s.Resource))
	ownPrefix := fmt.Sprintf("mti-nat-%x-", resourceHash[:8])
	ownAllow, ownDrop := 0, 0
	var allowTag, dropTag string
	for _, f := range filters {
		comment := f["comment"]
		if !strings.HasPrefix(comment, "mti-nat-") {
			continue
		}
		own := strings.HasPrefix(comment, ownPrefix)
		// Other managed rules may overlap if any match attribute is missing.
		overlaps := (f["chain"] == "" || f["chain"] == "forward") &&
			(f["in-interface"] == "" || f["in-interface"] == s.WAN) &&
			geoIPMayOverlap(f["dst-address"], s.Destination) &&
			geoProtocolMayOverlap(f["protocol"], s.Protocol) &&
			geoPortMayOverlap(f["dst-port"], s.Port)
		if !own && overlaps {
			return fmt.Errorf("обнаружен конфликт другого служебного белого списка; проверьте правила firewall вручную")
		}
		if own {
			if !overlaps || !geoFalse(f["disabled"]) || f["chain"] != "forward" || f["in-interface"] != s.WAN || (f["dst-address"] != s.Destination && f["dst-address"] != s.Destination+"/32") || geoProtocol(f["protocol"]) != s.Protocol || !geoSinglePort(f["dst-port"], s.Port) {
				return fmt.Errorf("существующие правила этого идентификатора отличаются или повреждены; проверьте firewall вручную")
			}
			if strings.HasSuffix(comment, "-allow") && f["action"] == "accept" {
				ownAllow++
				allowTag = strings.TrimSuffix(comment, "-allow")
			} else if strings.HasSuffix(comment, "-drop") && f["action"] == "drop" {
				ownDrop++
				dropTag = strings.TrimSuffix(comment, "-drop")
			} else {
				return fmt.Errorf("неизвестное служебное правило этого идентификатора; проверьте firewall вручную")
			}
		}
	}
	if ownAllow != ownDrop || ownAllow > 1 || allowTag != dropTag {
		return fmt.Errorf("найден неполный или повторный набор служебных правил; проверьте firewall вручную")
	}
	nats, err := read("/ip/firewall/nat")
	if err != nil {
		return fmt.Errorf("не удалось прочитать правила dst-nat; настройки не изменены")
	}
	var listMembers []map[string]string
	listsRead := false
	var rejected []string
	candidates := 0
	for _, n := range nats {
		if !geoSingleAddress(n["to-addresses"], dest) {
			continue
		}
		candidates++
		var reasons []string
		if n["chain"] != "dstnat" {
			reasons = append(reasons, "chain не dstnat")
		}
		if n["action"] != "dst-nat" {
			reasons = append(reasons, "action не dst-nat")
		}
		if !geoFalse(n["disabled"]) {
			if n["disabled"] == "" {
				reasons = append(reasons, "disabled не получено")
			} else if geoTrue(n["disabled"]) {
				reasons = append(reasons, "правило выключено")
			} else {
				reasons = append(reasons, "неизвестное значение disabled")
			}
		}
		if geoTrue(n["invalid"]) {
			reasons = append(reasons, "правило invalid")
		} else if n["invalid"] != "" && !geoFalse(n["invalid"]) {
			reasons = append(reasons, "неизвестное значение invalid")
		}
		if geoProtocol(n["protocol"]) != s.Protocol {
			if n["protocol"] == "" {
				reasons = append(reasons, "protocol не получен")
			} else if geoProtocol(n["protocol"]) == "" {
				reasons = append(reasons, "неизвестный формат protocol")
			} else {
				reasons = append(reasons, "другой protocol")
			}
		}
		port := n["to-ports"]
		if port == "" {
			port = n["dst-port"]
		}
		if !geoSinglePort(port, s.Port) {
			if port == "" {
				reasons = append(reasons, "не получен внутренний порт или dst-port")
			} else {
				reasons = append(reasons, "внутренний порт не совпал или задан диапазоном")
			}
		}
		iface, group := n["in-interface"], n["in-interface-list"]
		if iface != "" && iface != s.WAN {
			reasons = append(reasons, "другой in-interface")
		}
		if group != "" {
			if !listsRead {
				listMembers, err = read("/interface/list/member")
				if err != nil {
					return fmt.Errorf("не удалось проверить список WAN-интерфейсов; настройки не изменены")
				}
				listsRead = true
			}
			found := false
			for _, member := range listMembers {
				if member["list"] == group && member["interface"] == s.WAN && geoFalse(member["disabled"]) {
					found = true
					break
				}
			}
			if !found {
				reasons = append(reasons, "не подтверждён WAN в in-interface-list")
			}
		}
		if len(reasons) == 0 {
			return nil
		}
		if len(rejected) < 3 {
			rejected = append(rejected, strings.Join(reasons, ", "))
		}
	}
	message := "не найден однозначный включённый dst-nat на этот IPv4, внутренний порт, протокол и WAN; настройки не изменены"
	if candidates == 0 {
		return fmt.Errorf("%s. В ответе RouterOS нет записи с точным to-addresses", message)
	}
	return fmt.Errorf("%s. Записей с этим to-addresses: %d; причины отказа (до 3): %s", message, candidates, strings.Join(rejected, "; "))
}
