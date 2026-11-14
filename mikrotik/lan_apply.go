package mikrotik

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LANApplyRequest describes only LAN and optionally DHCP. The old generic
// generated script is deliberately not accepted here.
type LANApplyRequest struct {
	Conn         Params       `json:"conn"`
	Device       *Info        `json:"device"`
	Interfaces   []Iface      `json:"interfaces"`
	LAN          LanSettings  `json:"lan"`
	DHCP         DHCPSettings `json:"dhcp"`
	Acknowledged bool         `json:"acknowledged"`
	Fingerprint  string       `json:"fingerprint"`
	Script       string       `json:"script"`
}
type LANPlan struct {
	Summary     string `json:"summary"`
	Script      string `json:"script"`
	Fingerprint string `json:"fingerprint"`
}

var lanProperties = map[string]string{
	"/system/identity": "name,identity", "/system/resource": "version,architecture-name,board-name",
	"/interface": ".id,name,disabled", "/interface/bridge/port": ".id,interface,bridge,disabled",
	"/ip/address":     ".id,address,network,interface,actual-interface,disabled,dynamic,invalid",
	"/ip/dhcp-server": ".id,name,interface,address-pool,disabled,dynamic,invalid",
	"/ip/pool":        ".id,name,ranges,next-pool", "/ip/dhcp-server/network": ".id,address,gateway,dns-server",
}
var zeroPlanTime time.Time
var lanID = regexp.MustCompile(`^\*[0-9A-Fa-f]+$`)

func validLANID(v string) bool { return lanID.MatchString(v) }

// withLANSession uses one selected transport; it never retries another one.
func withLANSession(ctx context.Context, req LANApplyRequest, fn func(snapshotReader, func(string) error) (*LANPlan, error)) (*LANPlan, error) {
	p := req.Conn
	if err := p.normalize(); err != nil {
		return nil, err
	}
	if p.Protocol != "api" && p.Protocol != "rest" {
		return nil, fmt.Errorf("план и применение LAN доступны по API/REST; MAC не поддерживается")
	}
	if req.Device == nil || req.Device.Protocol != p.Protocol || req.Device.Identity == "" || req.Device.RouterOS == "" || req.Device.Architecture == "" {
		return nil, fmt.Errorf("подключитесь к устройству заново")
	}
	if strings.ContainsAny(req.Device.Model+req.Device.RouterOS, "\r\n\x00") {
		return nil, fmt.Errorf("некорректные данные устройства")
	}
	var read snapshotReader
	var run func(string) error
	if p.Protocol == "api" {
		c, err := dialAPI(ctx, &p)
		if err != nil {
			return nil, fmt.Errorf("не удалось подключиться для проверки LAN")
		}
		defer c.Close()
		if err = c.login(p.User, p.Password); err != nil {
			return nil, fmt.Errorf("не удалось авторизоваться для проверки LAN")
		}
		read = func(path string) ([]map[string]string, error) {
			props, ok := lanProperties[path]
			if !ok {
				return nil, fmt.Errorf("unknown path")
			}
			r, _, err := c.exec(path+"/print", "=.proplist="+props)
			return r, err
		}
		run = func(script string) error { return applyAPIClient(c, script) }
	} else {
		rc, err := newRESTClient(&p)
		if err != nil {
			return nil, fmt.Errorf("не удалось подготовить соединение")
		}
		read = func(path string) ([]map[string]string, error) {
			props, ok := lanProperties[path]
			if !ok {
				return nil, fmt.Errorf("unknown path")
			}
			return readRESTProperties(ctx, rc, path, props)
		}
		run = func(script string) error { return applyRESTClient(ctx, rc, script) }
	}
	if err := checkGeoDevice(req.Device, read); err != nil {
		return nil, err
	}
	return fn(read, run)
}
func PlanLAN(ctx context.Context, req LANApplyRequest) (*LANPlan, error) {
	return withLANSession(ctx, req, func(read snapshotReader, _ func(string) error) (*LANPlan, error) { return buildLANPlan(req, read) })
}
func ApplyLAN(ctx context.Context, req LANApplyRequest) (*LANPlan, error) {
	if !req.Acknowledged || len(req.Script) == 0 || len(req.Script) > maxScriptSize || len(req.Fingerprint) != 64 {
		return nil, fmt.Errorf("ознакомьтесь с планом и подтвердите резервную копию и локальный доступ")
	}
	return withLANSession(ctx, req, func(read snapshotReader, run func(string) error) (*LANPlan, error) {
		plan, err := buildLANPlan(req, read)
		if err != nil {
			return nil, err
		}
		if req.Script != plan.Script || req.Fingerprint != plan.Fingerprint {
			return nil, fmt.Errorf("конфигурация или параметры изменились; заново подготовьте план LAN")
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("проверка прервана до применения")
		}
		if err = run(plan.Script); err != nil {
			return nil, fmt.Errorf("выполнение LAN не завершилось; настройки могли измениться частично, доступ мог прерваться; проверьте роутер локально и не повторяйте вслепую")
		}
		return plan, nil
	})
}
func buildLANPlan(req LANApplyRequest, read snapshotReader) (*LANPlan, error) {
	settings := Settings{LAN: req.LAN, DHCP: req.DHCP}
	normalizeIPFields(&settings)
	settings.LAN.Interface = strings.TrimSpace(settings.LAN.Interface)
	settings.DHCP.Interface = strings.TrimSpace(settings.DHCP.Interface)
	if !settings.LAN.Enabled {
		return nil, fmt.Errorf("включите раздел Addresses")
	}
	generated := Generate(req.Device, req.Interfaces, &settings, zeroPlanTime)
	if !generated.OK {
		return nil, fmt.Errorf("параметры LAN: %s", generated.Error)
	}
	for _, s := range generated.Skipped {
		if s.ID == featLAN || s.ID == featDHCP {
			return nil, fmt.Errorf("раздел %s недоступен", s.Title)
		}
	}
	iface := settings.LAN.Interface
	ip, subnet, err := net.ParseCIDR(settings.LAN.Address)
	if err != nil || ip.To4() == nil {
		return nil, fmt.Errorf("некорректный LAN IPv4")
	}
	ip = ip.To4()
	newNetwork := subnet.String()
	newNetworkIP := subnet.IP.To4().String()
	rows := map[string][]map[string]string{}
	paths := []string{"/interface", "/interface/bridge/port", "/ip/address"}
	if settings.DHCP.Enabled {
		paths = append(paths, "/ip/dhcp-server", "/ip/pool", "/ip/dhcp-server/network")
	}
	for _, path := range paths {
		data, err := read(path)
		if err != nil || len(data) > 5000 {
			return nil, fmt.Errorf("не удалось полностью прочитать %s; настройки не изменены", path)
		}
		rows[path] = data
	}
	matches := 0
	for _, r := range rows["/interface"] {
		if r["name"] == iface && explicitlyFalse(r["disabled"]) {
			matches++
		}
	}
	if matches != 1 {
		return nil, fmt.Errorf("интерфейс отсутствует, выключен или его состояние неизвестно")
	}
	for _, r := range rows["/interface/bridge/port"] {
		if r["interface"] == "" || r["bridge"] == "" {
			return nil, fmt.Errorf("неполные данные о портах bridge; настройки не изменены")
		}
		if r["interface"] == iface {
			return nil, fmt.Errorf("интерфейс является портом bridge; выберите bridge вместо порта")
		}
	}
	var old map[string]string
	var oldNet *net.IPNet
	var oldIP net.IP
	for _, r := range rows["/ip/address"] {
		if r["interface"] == "" {
			return nil, fmt.Errorf("у одного из адресов не указан интерфейс; настройки не изменены")
		}
		host, n, e := net.ParseCIDR(r["address"])
		if e != nil || host.To4() == nil {
			return nil, fmt.Errorf("не удалось проверить один из IPv4-адресов")
		}
		if r["interface"] == iface {
			if old != nil || !validLANID(r[".id"]) || !explicitlyFalse(r["dynamic"]) || !explicitlyFalse(r["disabled"]) || !explicitlyFalse(r["invalid"]) {
				return nil, fmt.Errorf("LAN-адресов несколько или адрес динамический, выключен либо его состояние неизвестно")
			}
			if r["network"] != "" && r["network"] != n.IP.To4().String() {
				return nil, fmt.Errorf("текущий LAN-адрес имеет нестандартное поле network; требуется ручная проверка")
			}
			old = r
			oldNet = n
			oldIP = host.To4()
		} else if subnet.Contains(host) || n.Contains(ip) {
			return nil, fmt.Errorf("новая LAN-подсеть пересекается с адресом на другом интерфейсе")
		}
	}
	var b, addressCommand strings.Builder
	var summary []string
	b.WriteString("# MTI-LAN-GUARDED-APPLY\n# Без автоматического отката; применяется только подтверждённый план.\n")
	if old == nil {
		summary = append(summary, "Добавить адрес "+settings.LAN.Address+" на "+iface)
		fmt.Fprintf(&addressCommand, "/ip address add %s %s %s comment=mti-lan disabled=no\n", attr("address", settings.LAN.Address), attr("network", newNetworkIP), attr("interface", iface))
	} else {
		summary = append(summary, "Изменить адрес "+old["address"]+" на "+settings.LAN.Address+" на "+iface)
		fmt.Fprintf(&addressCommand, "/ip address set %s %s %s\n", old[".id"], attr("address", settings.LAN.Address), attr("network", newNetworkIP))
	}
	if !settings.DHCP.Enabled {
		summary = append(summary, "DHCP и DNS не меняются; проверьте их после смены адреса.")
	} else {
		var server map[string]string
		for _, r := range rows["/ip/dhcp-server"] {
			if r["interface"] != iface {
				return nil, fmt.Errorf("есть DHCP-сервер на другом интерфейсе; автоматический план пока не определяет общие сети и пулы")
			}
			if server != nil || r["name"] == "" || !validLANID(r[".id"]) || !explicitlyFalse(r["dynamic"]) || !explicitlyFalse(r["invalid"]) || (!explicitlyTrue(r["disabled"]) && !explicitlyFalse(r["disabled"])) {
				return nil, fmt.Errorf("не удалось однозначно выбрать DHCP-сервер на LAN")
			}
			server = r
		}
		pool := generated.Derived.Pool
		if settings.DHCP.PoolStart != "" {
			pool = settings.DHCP.PoolStart
			if settings.DHCP.PoolEnd != settings.DHCP.PoolStart {
				pool += "-" + settings.DHCP.PoolEnd
			}
		}
		h := sha256.Sum256([]byte(iface + "\x00" + pool))
		poolName := "mti-lan-" + hex.EncodeToString(h[:6])
		found := false
		for _, r := range rows["/ip/pool"] {
			if r["name"] == "" {
				return nil, fmt.Errorf("не удалось проверить имя одного из пулов")
			}
			if r["name"] == poolName {
				if found || r["ranges"] != pool || (r["next-pool"] != "" && r["next-pool"] != "none") {
					return nil, fmt.Errorf("имя нового пула уже занято другим пулом")
				}
				found = true
			}
		}
		if found {
			summary = append(summary, "Использовать существующий пул "+poolName+": "+pool)
		} else {
			summary = append(summary, "Добавить пул "+poolName+": "+pool)
			fmt.Fprintf(&b, "/ip pool add %s %s\n", attr("name", poolName), attr("ranges", pool))
		}
		var oldDHCPNet map[string]string
		for _, r := range rows["/ip/dhcp-server/network"] {
			_, n, e := net.ParseCIDR(r["address"])
			if e != nil || n.IP.To4() == nil {
				return nil, fmt.Errorf("не удалось проверить одну из DHCP-сетей")
			}
			isOld := oldNet != nil && n.String() == oldNet.String()
			if isOld {
				if oldDHCPNet != nil || !validLANID(r[".id"]) {
					return nil, fmt.Errorf("не удалось однозначно выбрать прежнюю DHCP-сеть")
				}
				oldDHCPNet = r
			} else if n.Contains(ip) || subnet.Contains(n.IP) {
				return nil, fmt.Errorf("новая LAN пересекается с другой DHCP-сетью")
			}
		}
		if oldDHCPNet == nil {
			summary = append(summary, "Добавить DHCP-сеть "+newNetwork+" со шлюзом "+ip.String()+"; DNS отдельно не задаётся.")
			fmt.Fprintf(&b, "/ip dhcp-server network add %s %s comment=mti-net\n", attr("address", newNetwork), attr("gateway", ip.String()))
		} else {
			summary = append(summary, "Изменить DHCP-сеть "+oldDHCPNet["address"]+" на "+newNetwork+", шлюз "+ip.String()+"; прочие поля сохраняются.")
			// Preserve DNS, except references to the previous gateway need the new address.
			dns := oldDHCPNet["dns-server"]
			if _, present := oldDHCPNet["dns-server"]; !present {
				summary = append(summary, "DHCP DNS не был прочитан; проверьте его вручную после изменения LAN.")
			}
			updated := false
			if oldIP != nil && dns != "" {
				parts := strings.Split(dns, ",")
				for i, v := range parts {
					if strings.TrimSpace(v) == oldIP.String() {
						parts[i] = ip.String()
						updated = true
					}
				}
				if updated {
					dns = strings.Join(parts, ",")
					summary = append(summary, "Заменить прежний адрес роутера в DHCP DNS на "+ip.String())
				}
			}
			fmt.Fprintf(&b, "/ip dhcp-server network set %s %s %s", oldDHCPNet[".id"], attr("address", newNetwork), attr("gateway", ip.String()))
			if updated {
				fmt.Fprintf(&b, " %s", attr("dns-server", dns))
			}
			b.WriteByte('\n')
		}
		if server == nil {
			name := "mti-dhcp-" + hex.EncodeToString(h[:4])
			summary = append(summary, "Добавить DHCP-сервер "+name+" на "+iface)
			fmt.Fprintf(&b, "/ip dhcp-server add %s %s %s %s disabled=no\n", attr("name", name), attr("interface", iface), attr("address-pool", poolName), attr("lease-time", strings.TrimSpace(settings.DHCP.LeaseTime)))
		} else {
			summary = append(summary, "Изменить DHCP-сервер "+server["name"]+": пул "+poolName+", аренда "+settings.DHCP.LeaseTime+", включить.")
			fmt.Fprintf(&b, "/ip dhcp-server set %s %s %s disabled=no\n", server[".id"], attr("address-pool", poolName), attr("lease-time", strings.TrimSpace(settings.DHCP.LeaseTime)))
		}
		summary = append(summary, "Прежний пул не удаляется; пересечения с другими пулами, существующие аренды и клиенты не проверяются.")
	}
	b.WriteString(addressCommand.String()) // Keep the old management address until DHCP steps finish.
	summary = append(summary, "Адрес меняется последним, но отказ может оставить DHCP частично изменённым.")
	summary = append(summary, "Изменение LAN может оборвать соединение. Отката нет; NAT, firewall, маршруты, DNS роутера и другие службы не меняются.")
	// Canonicalize read records so irrelevant response ordering does not invalidate a plan.
	for _, p := range paths {
		sort.Slice(rows[p], func(i, j int) bool {
			a, _ := json.Marshal(rows[p][i])
			c, _ := json.Marshal(rows[p][j])
			return string(a) < string(c)
		})
	}
	data, _ := json.Marshal(struct {
		LAN  LanSettings
		DHCP DHCPSettings
		Rows map[string][]map[string]string
	}{settings.LAN, settings.DHCP, rows})
	sum := sha256.Sum256(data)
	return &LANPlan{Summary: strings.Join(summary, "\n"), Script: b.String(), Fingerprint: hex.EncodeToString(sum[:])}, nil
}
