package mikrotik

import (
	"context"
	"fmt"
	"strings"
)

// LogEntry — запись журнала RouterOS.
type LogEntry struct {
	Time    string `json:"time"`
	Topics  string `json:"topics"`
	Message string `json:"message"`
}

// logDefaultLimit — сколько записей возвращать, если лимит не задан. Журнал
// читается только для диагностики, весь он не нужен.
const logDefaultLimit = 200

// logMaxLimit — верхняя граница запрошенного лимита. Больший лимит
// ограничивается этим значением, а не сбрасывается к значению по умолчанию:
// иначе запрос «последние 1000 записей» молча возвращал бы 200.
const logMaxLimit = 1000

// CollectLog возвращает последние записи журнала роутера, в которых встретился
// filter (без учёта регистра) — в сообщении или в темах. Пустой фильтр
// возвращает весь журнал, но не больше limit последних записей.
//
// Журнал — единственный способ узнать, как отработал применённый скрипт:
// вывод команд наружу не передаётся, поэтому скрипты пишут результат в /log.
func CollectLog(ctx context.Context, p Params, filter string, limit int) ([]LogEntry, error) {
	if err := p.normalize(); err != nil {
		return nil, err
	}
	if p.Protocol == "mac" {
		return nil, fmt.Errorf("журнал недоступен по MAC-Telnet — подключитесь по API или REST")
	}
	limit = logLimit(limit)
	var protocols []string
	switch p.Protocol {
	case "api":
		protocols = []string{"api"}
	case "rest":
		protocols = []string{"rest"}
	default:
		protocols = []string{"api", "rest"}
	}
	var errs []string
	for _, proto := range protocols {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err().Error())
			break
		}
		var (
			list []LogEntry
			err  error
		)
		switch proto {
		case "api":
			list, err = logAPI(ctx, &p, filter, limit)
		case "rest":
			list, err = logREST(ctx, &p, filter, limit)
		}
		if err == nil {
			return list, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %s", proto, err))
	}
	return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
}

func logAPI(ctx context.Context, p *Params, filter string, limit int) ([]LogEntry, error) {
	c, err := dialAPI(ctx, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	if err := c.login(p.User, p.Password); err != nil {
		return nil, err
	}
	recs, _, err := c.exec("/log/print")
	if err != nil {
		return nil, fmt.Errorf("чтение /log: %w", err)
	}
	out := make([]LogEntry, 0, len(recs))
	for _, m := range recs {
		e := LogEntry{Time: m["time"], Topics: m["topics"], Message: m["message"]}
		if e.Message == "" {
			continue
		}
		out = append(out, e)
	}
	return filterLog(out, filter, limit), nil
}

func logREST(ctx context.Context, p *Params, filter string, limit int) ([]LogEntry, error) {
	rc, err := newRESTClient(p)
	if err != nil {
		return nil, err
	}
	recs, err := rc.get(ctx, "/rest/log")
	if err != nil {
		return nil, err
	}
	out := make([]LogEntry, 0, len(recs))
	for _, m := range recs {
		e := LogEntry{Time: jstr(m, "time"), Topics: jstr(m, "topics"), Message: jstr(m, "message")}
		if e.Message == "" {
			continue
		}
		out = append(out, e)
	}
	return filterLog(out, filter, limit), nil
}

// logLimit приводит запрошенный лимит к допустимому диапазону.
func logLimit(limit int) int {
	switch {
	case limit <= 0:
		return logDefaultLimit
	case limit > logMaxLimit:
		return logMaxLimit
	default:
		return limit
	}
}

// filterLog оставляет записи, в которых есть filter, и возвращает не более
// limit последних — журнал роутера может быть длинным.
func filterLog(entries []LogEntry, filter string, limit int) []LogEntry {
	needle := strings.ToLower(strings.TrimSpace(filter))
	matched := entries
	if needle != "" {
		matched = nil
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e.Message), needle) || strings.Contains(strings.ToLower(e.Topics), needle) {
				matched = append(matched, e)
			}
		}
	}
	if len(matched) > limit {
		return matched[len(matched)-limit:]
	}
	return matched
}
