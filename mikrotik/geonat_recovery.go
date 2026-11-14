package mikrotik

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// GeoRecoveryRequest targets only one incomplete mti-nat set, never dst-nat.
type GeoRecoveryRequest struct {
	Conn         Params         `json:"conn"`
	Device       *Info          `json:"device"`
	Geo          GeoNATSettings `json:"geoNat"`
	Acknowledged bool           `json:"acknowledged"`
}
type geoRecoveryPlan struct {
	filterID   string
	addressIDs []string
	tag        string
}

var geoRecoveryTag = regexp.MustCompile(`^mti-nat-[0-9a-f]{16}-[0-9a-f]{10}$`)
var geoRecoveryProperties = map[string]string{
	"/system/identity": "name,identity", "/system/resource": "version,architecture-name,board-name",
	"/ip/firewall/filter": ".id,comment,chain,action,connection-nat-state,protocol,dst-address,dst-port,in-interface,src-address-list,dst-address-list",
	"/ip/firewall/nat":    ".id,src-address-list,dst-address-list", "/ip/firewall/mangle": ".id,src-address-list,dst-address-list",
	"/ip/firewall/raw": ".id,src-address-list,dst-address-list", "/ip/firewall/address-list": ".id,list,comment,dynamic",
}

// RecoverGeoNAT removes one uniquely identified partial allow and its own list.
// It does not apply a new whitelist or alter dst-nat. Other RouterOS clients can
// change objects concurrently; after the first remove any failure is partial.
func RecoverGeoNAT(ctx context.Context, req GeoRecoveryRequest) (int, error) {
	p := req.Conn
	if err := p.normalize(); err != nil {
		return 0, fmt.Errorf("некорректное подключение")
	}
	if p.Protocol != "api" && p.Protocol != "rest" {
		return 0, fmt.Errorf("очистка доступна по API/REST")
	}
	if !req.Acknowledged || req.Device == nil || req.Device.Protocol != p.Protocol || req.Device.Identity == "" || req.Device.RouterOS == "" || req.Device.Architecture == "" || !strings.HasPrefix(req.Device.RouterOS, "7.") {
		return 0, fmt.Errorf("подключитесь заново и подтвердите очистку и резервную копию")
	}
	g := req.Geo
	ip := net.ParseIP(g.Destination)
	if !g.Enabled || !geoResourceRE.MatchString(g.Resource) || strings.TrimSpace(g.WAN) == "" || len(g.WAN) > 128 || strings.ContainsAny(g.WAN, "\r\n\x00") || ip == nil || ip.To4() == nil || !portNumber(g.Port) || (g.Protocol != "tcp" && g.Protocol != "udp") {
		return 0, fmt.Errorf("проверьте идентификатор, WAN, IPv4, порт и протокол")
	}
	var read snapshotReader
	var remove func(string, string) error
	if p.Protocol == "api" {
		c, err := dialAPI(ctx, &p)
		if err != nil {
			return 0, fmt.Errorf("подключение для очистки не удалось")
		}
		defer c.Close()
		if err = c.login(p.User, p.Password); err != nil {
			return 0, fmt.Errorf("авторизация для очистки не удалась")
		}
		c.opTimeout = 30 * time.Second
		read = func(path string) ([]map[string]string, error) {
			props, ok := geoRecoveryProperties[path]
			if !ok {
				return nil, fmt.Errorf("invalid path")
			}
			rows, _, err := c.exec(path+"/print", "=.proplist="+props)
			return rows, err
		}
		remove = func(path, id string) error { _, _, err := c.exec(path+"/remove", "=numbers="+id); return err }
	} else {
		rc, err := newRESTClient(&p)
		if err != nil {
			return 0, fmt.Errorf("подключение для очистки не удалось")
		}
		read = func(path string) ([]map[string]string, error) {
			props, ok := geoRecoveryProperties[path]
			if !ok {
				return nil, fmt.Errorf("invalid path")
			}
			return readRESTProperties(ctx, rc, path, props)
		}
		remove = func(path, id string) error {
			_, err := rc.post(ctx, "/rest"+path+"/remove", map[string]string{"numbers": id})
			return err
		}
	}
	if err := checkGeoDevice(req.Device, read); err != nil {
		return 0, err
	}
	plan, err := planGeoRecovery(g, read)
	if err != nil {
		return 0, err
	}
	// Re-read immediately before first write; other clients are still not locked.
	again, err := planGeoRecovery(g, read)
	if err != nil || again.tag != plan.tag || again.filterID != plan.filterID || !slices.Equal(again.addressIDs, plan.addressIDs) {
		return 0, fmt.Errorf("набор или ссылки изменились при проверке; настройки не изменены")
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("очистка прервана до записи")
	}
	// Never replay after a remove, even if the response is lost.
	if err = remove("/ip/firewall/filter", plan.filterID); err != nil {
		return 0, fmt.Errorf("очистка не завершена: правило могло измениться; проверьте firewall и список адресов вручную, не повторяйте вслепую")
	}
	for _, id := range plan.addressIDs {
		if err = remove("/ip/firewall/address-list", id); err != nil {
			return 0, fmt.Errorf("очистка не завершена: изменения могли быть частичными; проверьте firewall и список адресов вручную, не повторяйте вслепую")
		}
	}
	filters, err1 := read("/ip/firewall/filter")
	lists, err2 := read("/ip/firewall/address-list")
	if err1 != nil || err2 != nil {
		return 0, fmt.Errorf("удаление отправлено, но итог не подтверждён; проверьте firewall и список адресов вручную")
	}
	for _, f := range filters {
		if f["comment"] == plan.tag+"-allow" {
			return 0, fmt.Errorf("итог очистки не подтверждён; проверьте firewall вручную")
		}
	}
	for _, r := range lists {
		if r["list"] == plan.tag || r["comment"] == plan.tag {
			return 0, fmt.Errorf("итог очистки не подтверждён; проверьте список адресов вручную")
		}
	}
	return len(plan.addressIDs), nil
}

func geoRecoveryAddressMatches(value, target string) bool {
	if geoSingleAddress(value, net.ParseIP(target)) {
		return true
	}
	host, block, err := net.ParseCIDR(value)
	if err != nil || host.To4() == nil {
		return false
	}
	ones, bits := block.Mask.Size()
	return bits == 32 && ones == 32 && host.Equal(net.ParseIP(target))
}

func planGeoRecovery(g GeoNATSettings, read snapshotReader) (geoRecoveryPlan, error) {
	fail := func() (geoRecoveryPlan, error) {
		return geoRecoveryPlan{}, fmt.Errorf("не найден один однозначный неполный служебный набор или он используется другими правилами; настройки не изменены")
	}
	sum := sha256.Sum256([]byte(g.Resource))
	prefix := fmt.Sprintf("mti-nat-%x-", sum[:8])
	filters, err := read("/ip/firewall/filter")
	if err != nil || len(filters) > 10000 {
		return geoRecoveryPlan{}, fmt.Errorf("не удалось прочитать firewall; настройки не изменены")
	}
	var selected map[string]string
	selectedIndex := -1
	for i, f := range filters {
		if strings.HasPrefix(f["comment"], prefix) {
			if selected != nil {
				return fail()
			}
			selected = f
			selectedIndex = i
		}
	}
	if selected == nil || !strings.HasSuffix(selected["comment"], "-allow") {
		return fail()
	}
	tag := strings.TrimSuffix(selected["comment"], "-allow")
	if !geoRecoveryTag.MatchString(tag) || !validGeoNATID(selected[".id"]) || selected["chain"] != "forward" || selected["action"] != "accept" || selected["connection-nat-state"] != "dstnat" || geoProtocol(selected["protocol"]) != g.Protocol || !geoRecoveryAddressMatches(selected["dst-address"], g.Destination) || !geoSinglePort(selected["dst-port"], g.Port) || selected["in-interface"] != g.WAN || selected["src-address-list"] != tag || selected["dst-address-list"] != "" {
		return fail()
	}
	refers := func(r map[string]string) bool {
		for _, key := range []string{"src-address-list", "dst-address-list"} {
			if r[key] == tag || r[key] == "!"+tag {
				return true
			}
		}
		return false
	}
	for i, f := range filters {
		if i == selectedIndex {
			continue
		}
		if f[".id"] == selected[".id"] || refers(f) {
			return fail()
		}
	}
	for _, path := range []string{"/ip/firewall/nat", "/ip/firewall/mangle", "/ip/firewall/raw"} {
		rows, e := read(path)
		if e != nil || len(rows) > 10000 {
			return geoRecoveryPlan{}, fmt.Errorf("не удалось проверить ссылки на список; настройки не изменены")
		}
		for _, r := range rows {
			if refers(r) {
				return fail()
			}
		}
	}
	entries, err := read("/ip/firewall/address-list")
	if err != nil || len(entries) > 10000 {
		return geoRecoveryPlan{}, fmt.Errorf("не удалось проверить список адресов; настройки не изменены")
	}
	plan := geoRecoveryPlan{filterID: selected[".id"], tag: tag}
	seen := map[string]bool{}
	for _, r := range entries {
		if r["list"] != tag && r["comment"] != tag {
			continue
		}
		id := r[".id"]
		if r["list"] != tag || r["comment"] != tag || !validGeoNATID(id) || seen[id] || (r["dynamic"] != "" && !geoFalse(r["dynamic"])) {
			return fail()
		}
		seen[id] = true
		plan.addressIDs = append(plan.addressIDs, id)
	}
	if len(plan.addressIDs) == 0 || len(plan.addressIDs) > 2000 {
		return fail()
	}
	sort.Strings(plan.addressIDs)
	return plan, nil
}
