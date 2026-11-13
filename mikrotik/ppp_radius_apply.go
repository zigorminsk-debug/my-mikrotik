package mikrotik

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PPPRadiusApplyRequest is a specific generated preview, not arbitrary code.
type PPPRadiusApplyRequest struct {
	Conn        Params            `json:"conn"`
	Device      *Info             `json:"device"`
	Interfaces  []Iface           `json:"interfaces"`
	Radius      PPPRadiusSettings `json:"pppRadius"`
	GeneratedAt time.Time         `json:"generatedAt"`
	Script      string            `json:"script"`
}

var pppRadiusProperties = map[string]string{
	"/system/identity":              "name,identity",
	"/system/resource":              "version,architecture-name,board-name",
	"/interface/l2tp-server/server": "enabled,use-ipsec,authentication",
	"/ppp/aaa":                      "use-radius,accounting",
	"/radius":                       ".id",
}

// Read only specified fields; do not return a router's error body, which may
// contain sensitive configuration. Supports singleton REST object or arrays.
func radiusRESTRead(ctx context.Context, rc *restClient, path string) ([]map[string]string, error) {
	props, ok := pppRadiusProperties[path]
	if !ok {
		return nil, fmt.Errorf("неизвестный раздел")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rc.base+"/rest"+path+"?.proplist="+url.QueryEscape(props), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(rc.user, rc.pass)
	resp, err := rc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("отказ чтения")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, fmt.Errorf("ошибка чтения")
	}
	// A null/invalid response cannot prove that there are no existing rules.
	if !json.Valid(data) || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, fmt.Errorf("неверный ответ")
	}
	var raw []map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err = dec.Decode(&raw); err != nil {
		var m map[string]any
		dec = json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err = dec.Decode(&m); err != nil || m == nil {
			return nil, fmt.Errorf("неверный ответ")
		}
		raw = []map[string]any{m}
	}
	out := make([]map[string]string, 0, len(raw))
	for _, rec := range raw {
		m := map[string]string{}
		for _, k := range strings.Split(props, ",") {
			if _, ok := rec[k]; ok {
				m[k] = jstr(rec, k)
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func checkPPPRadiusPreflight(read snapshotReader) error {
	l2tp, err := read("/interface/l2tp-server/server")
	if err != nil || len(l2tp) != 1 {
		return fmt.Errorf("не удалось прочитать L2TP-сервер; настройки не изменены")
	}
	if !explicitlyTrue(l2tp[0]["enabled"]) {
		return fmt.Errorf("L2TP-сервер выключен или его состояние неизвестно; настройки не изменены")
	}
	if l2tp[0]["use-ipsec"] != "yes" && l2tp[0]["use-ipsec"] != "required" {
		return fmt.Errorf("L2TP/IPsec не включён или его состояние неизвестно; настройки не изменены")
	}
	aaa, err := read("/ppp/aaa")
	if err != nil || len(aaa) != 1 || (!explicitlyTrue(aaa[0]["use-radius"]) && !explicitlyFalse(aaa[0]["use-radius"])) {
		return fmt.Errorf("не удалось однозначно проверить PPP AAA; настройки не изменены")
	}
	rad, err := read("/radius")
	if err != nil {
		return fmt.Errorf("не удалось проверить существующие записи RADIUS; настройки не изменены")
	}
	if len(rad) > 0 {
		return fmt.Errorf("уже есть записи RADIUS; проверьте их вручную: автоматическое применение остановлено до изменений")
	}
	return nil
}

// ApplyPPPRadius deliberately never retries once writes start. The device
// snapshot and preflight are not an atomic transaction or reachability test.
func ApplyPPPRadius(ctx context.Context, req PPPRadiusApplyRequest) (string, error) {
	p := req.Conn
	if err := p.normalize(); err != nil {
		return "", err
	}
	if p.Protocol != "api" && p.Protocol != "rest" {
		return "", fmt.Errorf("автоприменение RADIUS доступно по API/REST; подключитесь к роутеру по IP")
	}
	if req.Device == nil || req.Device.Protocol != p.Protocol || req.Device.Identity == "" || req.Device.RouterOS == "" || req.Device.Architecture == "" {
		return "", fmt.Errorf("нет данных последнего подключения; подключитесь заново")
	}
	if strings.ContainsAny(req.Device.Model+req.Device.RouterOS, "\r\n\x00") {
		return "", fmt.Errorf("некорректные данные устройства")
	}
	if !req.Radius.Enabled || !req.Radius.Acknowledged || req.GeneratedAt.IsZero() {
		return "", fmt.Errorf("подтвердите резервную копию и обновите предпросмотр")
	}
	if len(req.Script) == 0 || len(req.Script) > maxScriptSize {
		return "", fmt.Errorf("неверный размер скрипта")
	}
	result := Generate(req.Device, req.Interfaces, &Settings{PPPRadius: req.Radius}, req.GeneratedAt)
	if !result.OK {
		return "", fmt.Errorf("проверка параметров: %s", result.Error)
	}
	if result.Script != req.Script || !strings.Contains(result.Script, "# MTI-PPP-RADIUS-GUARDED-APPLY") {
		return "", fmt.Errorf("предпросмотр изменился; обновите его")
	}
	var read snapshotReader
	var run func() error
	if p.Protocol == "api" {
		c, err := dialAPI(ctx, &p)
		if err != nil {
			return "", fmt.Errorf("не удалось подключиться для проверки")
		}
		defer c.Close()
		if err = c.login(p.User, p.Password); err != nil {
			return "", fmt.Errorf("не удалось авторизоваться для проверки")
		}
		read = func(path string) ([]map[string]string, error) {
			props, ok := pppRadiusProperties[path]
			if !ok {
				return nil, fmt.Errorf("неизвестный раздел")
			}
			recs, _, err := c.exec(path+"/print", "=.proplist="+props)
			return recs, err
		}
		run = func() error { return applyAPIClient(c, req.Script) }
	} else {
		rc, err := newRESTClient(&p)
		if err != nil {
			return "", fmt.Errorf("не удалось подготовить подключение")
		}
		read = func(path string) ([]map[string]string, error) { return radiusRESTRead(ctx, rc, path) }
		run = func() error { return applyRESTClient(ctx, rc, req.Script) }
	}
	if err := checkGeoDevice(req.Device, read); err != nil {
		return "", err
	}
	if err := checkPPPRadiusPreflight(read); err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("проверка прервана до применения")
	}
	if err := run(); err != nil {
		return "", fmt.Errorf("применение не завершилось; настройки могли измениться частично. Не повторяйте до проверки /radius и /ppp aaa")
	}
	return p.Protocol, nil
}
