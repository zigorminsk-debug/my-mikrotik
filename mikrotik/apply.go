package mikrotik

import (
	"context"
	"fmt"
	"strings"
)

// Имя служебного скрипта, который создаётся на роутере для применения конфигурации.
const applyScriptName = "mti-apply"

// Права скрипта: полный набор, чтобы конфигурация применялась без ограничений.
const applyScriptPolicy = "ftp,reboot,read,write,policy,test,password,sniff,sensitive,romon"

const maxScriptSize = 64 << 10 // 64 КБ

// ApplyScript применяет сформированный скрипт на роутере: создаёт его как
// /system/script и выполняет — в точности то же, что сделал бы пользователь,
// вставив код в New Terminal.
//
// При Protocol="auto" пробует RouterOS API, затем REST.
func ApplyScript(ctx context.Context, p Params, source string) (string, error) {
	if err := p.normalize(); err != nil {
		return "", err
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("скрипт пуст")
	}
	if len(source) > maxScriptSize {
		return "", fmt.Errorf("скрипт слишком большой (%d ББ, максимум %d)", len(source), maxScriptSize)
	}

	if p.Protocol == "mac" {
		if err := applyMAC(ctx, &p, source); err != nil {
			return "", err
		}
		return "mac", nil
	}

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
		var err error
		switch proto {
		case "api":
			err = applyAPI(ctx, &p, source)
		case "rest":
			err = applyREST(ctx, &p, source)
		}
		if err == nil {
			return proto, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %s", proto, err))
	}
	return "", fmt.Errorf("%s", strings.Join(errs, "; "))
}

// applyAPI: remove (игнорируем ошибку) → add → run через протокол RouterOS API.
func applyAPI(ctx context.Context, p *Params, source string) error {
	c, err := dialAPI(ctx, p)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.login(p.User, p.Password); err != nil {
		return err
	}

	// Старый скрипт может отсутствовать — ошибку этого шага игнорируем.
	// Имя в numbers понимают не все версии, поэтому сначала пробуем по .id:
	// иначе повторное применение после неудачи падает на создании скрипта
	// («already exists») вместо настоящей причины.
	if recs, _, err := c.exec("/system/script/print", "?name="+applyScriptName, "=.proplist=.id"); err == nil {
		for _, m := range recs {
			if id := m[".id"]; id != "" {
				_, _, _ = c.exec("/system/script/remove", "=numbers="+id)
			}
		}
	}
	_, _, _ = c.exec("/system/script/remove", "=numbers="+applyScriptName)

	if _, _, err := c.exec("/system/script/add",
		"=name="+applyScriptName,
		"=source="+source,
		"=policy="+applyScriptPolicy,
	); err != nil {
		return fmt.Errorf("не удалось создать скрипт: %w", err)
	}

	// Аргумент команды run называется number (проверено по /console/inspect);
	// numbers — запасной вариант на случай других версий RouterOS.
	_, _, runErr := c.exec("/system/script/run", "=number="+applyScriptName)
	if runErr != nil {
		_, _, altErr := c.exec("/system/script/run", "=numbers="+applyScriptName)
		if altErr != nil {
			return fmt.Errorf("не удалось выполнить скрипт: %v; запасной вариант: %v", runErr, altErr)
		}
	}
	return nil
}

// applyREST: POST /rest/system/script/{remove,add,run}.
func applyREST(ctx context.Context, p *Params, source string) error {
	rc, err := newRESTClient(p)
	if err != nil {
		return err
	}

	// Удаление старого скрипта — может отсутствовать, ошибку игнорируем.
	// Снимаем по .id: оставшийся после неудачи скрипт иначе не даёт создать
	// новый (HTTP 400 «already exists»).
	if list, err := rc.get(ctx, "/rest/system/script?name="+applyScriptName); err == nil {
		for _, m := range list {
			if id := jstr(m, ".id"); id != "" {
				_, _ = rc.post(ctx, "/rest/system/script/remove", map[string]any{"numbers": id})
			}
		}
	}
	_, _ = rc.post(ctx, "/rest/system/script/remove", map[string]any{"numbers": applyScriptName})

	if _, err := rc.post(ctx, "/rest/system/script", map[string]any{
		"name":   applyScriptName,
		"source": source,
		"policy": applyScriptPolicy,
	}); err != nil {
		return fmt.Errorf("не удалось создать скрипт: %w", err)
	}

	// По данным сообщества MikroTik, run принимает имя скрипта в поле number.
	if _, err := rc.post(ctx, "/rest/system/script/run", map[string]any{
		"number": applyScriptName,
	}); err != nil {
		return fmt.Errorf("не удалось выполнить скрипт: %w", err)
	}
	return nil
}

// ScriptName возвращает имя служебного скрипта (для документации/интерфейса).
func ScriptName() string { return applyScriptName }
