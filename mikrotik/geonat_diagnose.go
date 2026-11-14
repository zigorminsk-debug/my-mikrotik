package mikrotik

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// DiagnoseGeoNAT performs the same read-only device/NAT checks as ApplyGeoNAT,
// without generating, creating or executing a script. A successful check is
// not proof of reachability, NAT precedence or firewall safety.
func DiagnoseGeoNAT(ctx context.Context, req GeoApplyRequest) (bool, string, error) {
	p := req.Conn
	if err := p.normalize(); err != nil {
		return false, "", err
	}
	if p.Protocol != "api" && p.Protocol != "rest" {
		return false, "", fmt.Errorf("проверка доступна по API/REST")
	}
	d := req.Device
	if d == nil || d.Protocol != p.Protocol || d.Identity == "" || d.RouterOS == "" || d.Architecture == "" {
		return false, "", fmt.Errorf("нет данных последнего подключения; подключитесь заново")
	}
	if !strings.HasPrefix(d.RouterOS, "7.") {
		return false, "", fmt.Errorf("проверка белого списка поддерживается только на RouterOS 7")
	}
	g := req.Geo
	if !g.Enabled || !geoResourceRE.MatchString(g.Resource) || strings.TrimSpace(g.WAN) == "" || len(g.WAN) > 128 || strings.ContainsAny(g.WAN, "\r\n\x00") || net.ParseIP(g.Destination) == nil || net.ParseIP(g.Destination).To4() == nil || !portNumber(g.Port) || (g.Protocol != "tcp" && g.Protocol != "udp") {
		return false, "", fmt.Errorf("проверьте идентификатор, WAN, IPv4, порт и протокол")
	}
	var read snapshotReader
	if p.Protocol == "api" {
		c, err := dialAPI(ctx, &p)
		if err != nil {
			return false, "", fmt.Errorf("не удалось подключиться для проверки")
		}
		defer c.Close()
		if err = c.login(p.User, p.Password); err != nil {
			return false, "", fmt.Errorf("не удалось авторизоваться для проверки")
		}
		read = func(path string) ([]map[string]string, error) {
			rows, _, err := c.exec(path + "/print")
			return rows, err
		}
		if err = checkGeoDevice(d, read); err != nil {
			return false, "", err
		}
		if err = checkGeoNAT(g, read); err != nil {
			return false, err.Error(), nil
		}
	} else {
		rc, err := newRESTClient(&p)
		if err != nil {
			return false, "", fmt.Errorf("не удалось подготовить подключение")
		}
		read = func(path string) ([]map[string]string, error) {
			records, err := rc.get(ctx, "/rest"+path)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]string, 0, len(records))
			for _, record := range records {
				m := map[string]string{}
				for k := range record {
					m[k] = jstr(record, k)
				}
				out = append(out, m)
			}
			return out, nil
		}
		if err = checkGeoDevice(d, read); err != nil {
			return false, "", err
		}
		if err = checkGeoNAT(g, read); err != nil {
			return false, err.Error(), nil
		}
	}
	return true, "Совпадающее правило найдено. Это не подтверждает его приоритет, доступность службы или защиту. Настройки не изменены.", nil
}
