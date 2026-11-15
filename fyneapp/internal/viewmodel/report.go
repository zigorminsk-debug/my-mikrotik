// Package viewmodel formats read-only diagnostics for the native desktop.
package viewmodel

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
)

// safe is for router-supplied text; it cannot create extra report lines.
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
}
func Info(i *mikrotik.Info) string {
	if i == nil {
		return "Нет данных"
	}
	var b strings.Builder
	for _, v := range [][2]string{{"Identity", i.Identity}, {"Модель", i.Model}, {"Плата", i.Board}, {"Архитектура", i.Architecture}, {"RouterOS", i.RouterOS}, {"Прошивка", i.CurrentFirmware}, {"Заводская прошивка", i.FactoryFirmware}, {"Доступная прошивка", i.UpgradeFirmware}, {"Время работы", i.Uptime}, {"CPU", i.CPU}, {"Протокол", i.Protocol}} {
		if v[1] != "" {
			fmt.Fprintf(&b, "%s: %s\n", v[0], safe(v[1]))
		}
	}
	if i.Diagnostics != nil {
		fmt.Fprintf(&b, "\nИсточники модели:\n  resource board-name: %s\n  RouterBOARD model: %s\n  RouterBOARD board: %s\n  Статус RouterBOARD: %s\n", safe(i.Diagnostics.ResourceBoard), safe(i.Diagnostics.RouterboardModel), safe(i.Diagnostics.RouterboardBoard), safe(i.Diagnostics.RouterboardStatus))
	}
	b.WriteString("\nПакеты:\n")
	if len(i.Packages) == 0 {
		b.WriteString("  Нет данных\n")
	}
	for _, p := range i.Packages {
		fmt.Fprintf(&b, "  %s %s  %s\n", safe(p.Name), safe(p.Version), safe(p.Status))
	}
	b.WriteString("\nСведения об устройстве не доказывают физическую идентичность.")
	return b.String()
}

var titles = map[string]string{"interfaces": "Интерфейсы", "addresses": "IPv4-адреса", "dhcp": "DHCP-клиенты роутера", "routes": "Маршруты по умолчанию и mti-probe-link", "scheduler": "Планировщик mti-failover", "environment": "Переменные mti-failover", "dhcpServers": "DHCP-серверы", "pools": "IPv4-пулы", "dhcpNetworks": "DHCP-сети", "bridges": "Bridge", "bridgePorts": "Порты bridge", "bridgeVLANs": "VLAN bridge", "vlanInterfaces": "VLAN-интерфейсы", "dhcpLeases": "Аренды DHCP-сервера", "failoverHistory": "История контроллера", "lte": "LTE"}
var order = []string{"interfaces", "addresses", "dhcp", "routes", "scheduler", "environment", "dhcpServers", "pools", "dhcpNetworks", "bridges", "bridgePorts", "bridgeVLANs", "vlanInterfaces", "dhcpLeases", "failoverHistory", "lte"}

func Snapshot(s *mikrotik.NetworkSnapshot) string {
	if s == nil {
		return "Нет данных"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Снимок сети (только чтение)\nПротокол: %s\nНачало UTC: %s\nКонец UTC: %s\n\n", safe(s.Protocol), s.StartedAt.UTC().Format("2006-01-02 15:04:05"), s.FinishedAt.UTC().Format("2006-01-02 15:04:05"))
	for _, id := range order {
		sec, ok := s.Sections[id]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%s:\n", titles[id])
		switch sec.Status {
		case "ok":
			if len(sec.Rows) == 0 {
				b.WriteString("  Записей не найдено.\n")
			}
			for n, row := range sec.Rows {
				keys := make([]string, 0, len(row))
				for k := range row {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				fmt.Fprintf(&b, "  %d. ", n+1)
				for j, k := range keys {
					if j > 0 {
						b.WriteString("; ")
					}
					fmt.Fprintf(&b, "%s=%s", safe(k), safe(row[k]))
				}
				b.WriteByte('\n')
			}
			if sec.Truncated {
				b.WriteString("  Результат усечён.\n")
			}
		case "error":
			b.WriteString("  Ошибка чтения (права, версия или связь).\n")
		case "unsupported":
			b.WriteString("  Недоступно для этой версии RouterOS.\n")
		default:
			b.WriteString("  Не прочитано.\n")
		}
		b.WriteByte('\n')
	}
	b.WriteString("Данные прочитаны последовательно, не атомарно. Отсутствующие поля неизвестны. Это не полная топология и не проверка интернета или доступности клиентов. Аренда DHCP не доказывает, что устройство онлайн. Отчёт может содержать частные IP, MAC и имена; проверьте перед передачей.")
	return b.String()
}
