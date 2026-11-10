package mikrotik

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// CLI-команды поверх MAC-Telnet: после аутентификации сессия — это терминал
// RouterOS, поэтому Collect/CollectInterfaces/ApplyScript выполняются
// обычными CLI-строками, а структурированный вывод забирается через
// :put [/path print as-value].

// macDial подключается к роутеру по MAC и возвращает сессию терминала.
func macDial(ctx context.Context, p *Params) (*macSession, error) {
	target, err := net.ParseMAC(p.Host)
	if err != nil {
		return nil, fmt.Errorf("некорректный MAC-адрес %q", p.Host)
	}
	sess, err := dialMAC(ctx, target, p.User, p.Password)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// macPrint выполняет :put [/path print as-value] и возвращает записи.
func macPrint(sess *macSession, path string) ([]map[string]string, error) {
	cmd := ":put [" + path + " print as-value without-paging]"
	out, err := sess.execTerminal(cmd, 15*time.Second)
	if err != nil {
		return nil, err
	}
	return parseAsValue(out), nil
}

// parseAsValue разбирает вывод :put ... as-value: одна строка на запись,
// пары key=value через ';'. Эхо и промпт отфильтровываются вызывающим кодом.
func parseAsValue(out string) []map[string]string {
	var records []map[string]string
	for _, line := range strings.FieldsFunc(out, func(r rune) bool { return r == '\r' || r == '\n' }) {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		m := parseAsValueLine(line)
		if len(m) > 0 {
			records = append(records, m)
		}
	}
	return records
}

func parseAsValueLine(line string) map[string]string {
	m := make(map[string]string)
	var pairs []string
	var cur strings.Builder
	inQuote := false
	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ';' && !inQuote:
			pairs = append(pairs, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	pairs = append(pairs, cur.String())
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.Trim(strings.TrimSpace(kv[1]), "\"")
		m[key] = val
	}
	return m
}

// collectMAC собирает информацию о роутере через MAC-Telnet терминал.
func collectMAC(ctx context.Context, p *Params) (*Info, error) {
	sess, err := macDial(ctx, p)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	info := &Info{Protocol: "mac", Packages: []Package{}}

	if recs, err := macPrint(sess, "/system identity"); err == nil && len(recs) > 0 {
		info.Identity = recs[0]["identity"]
	}
	if recs, err := macPrint(sess, "/system resource"); err == nil && len(recs) > 0 {
		m := recs[0]
		info.Model = m["board-name"]
		info.Architecture = m["architecture-name"]
		info.RouterOS = m["version"]
		info.Uptime = m["uptime"]
		info.CPU = m["cpu"]
	}
	// /system routerboard есть не на всех устройствах — ошибку игнорируем.
	if recs, err := macPrint(sess, "/system routerboard"); err == nil && len(recs) > 0 {
		m := recs[0]
		info.Board = m["board"]
		info.CurrentFirmware = m["current-firmware"]
		if info.CurrentFirmware == "" {
			info.CurrentFirmware = m["firmware"]
		}
		info.FactoryFirmware = m["factory-firmware"]
		info.UpgradeFirmware = m["upgrade-firmware"]
	}
	if recs, err := macPrint(sess, "/system package"); err == nil {
		for _, m := range recs {
			info.Packages = append(info.Packages, Package{
				Name:    m["name"],
				Version: m["version"],
				Status:  m["status"],
			})
		}
	}
	if info.Model == "" {
		info.Model = info.Board
	}
	return info, nil
}

// interfacesMAC возвращает список интерфейсов через MAC-Telnet терминал.
func interfacesMAC(ctx context.Context, p *Params) ([]Iface, error) {
	sess, err := macDial(ctx, p)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	recs, err := macPrint(sess, "/interface")
	if err != nil {
		return nil, fmt.Errorf("чтение /interface: %w", err)
	}
	out := make([]Iface, 0, len(recs))
	for _, m := range recs {
		iface := ifaceFromAttrs(m)
		if iface.Name == "" {
			continue
		}
		out = append(out, iface)
	}
	return out, nil
}

// applyMAC применяет скрипт построчно через терминал MAC-Telnet. Сгенерированные
// скрипты состоят из простых однострочных команд, поэтому построчная отправка
// с ожиданием промпта после каждой строки эквивалентна вставке в New Terminal.
func applyMAC(ctx context.Context, p *Params, source string) error {
	sess, err := macDial(ctx, p)
	if err != nil {
		return err
	}
	defer sess.Close()

	for _, line := range strings.Split(source, "\n") {
		if err := ctx.Err(); err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, err := sess.execTerminal(line, 20*time.Second); err != nil {
			return fmt.Errorf("выполнение %q: %w", line, err)
		}
	}
	return nil
}
