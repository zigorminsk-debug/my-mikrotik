package mikrotik

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const restTimeout = 15 * time.Second

// restClient — клиент REST API RouterOS (порт 80/443, Basic Auth).
type restClient struct {
	base   string
	client *http.Client
	user   string
	pass   string
}

func newRESTClient(p *Params) (*restClient, error) {
	scheme := "http"
	if p.TLS {
		scheme = "https"
	}
	hostPort := net.JoinHostPort(p.Host, strconv.Itoa(restPort(p)))
	u := url.URL{Scheme: scheme, Host: hostPort}
	return &restClient{
		base: u.String(),
		client: &http.Client{
			Timeout: restTimeout,
			Transport: &http.Transport{
				TLSClientConfig: routerTLSConfig(),
			},
		},
		user: p.User,
		pass: p.Password,
	}, nil
}

// get выполняет GET-запрос к /rest/... и декодирует JSON-массив записей.
func (rc *restClient) get(ctx context.Context, path string) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rc.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(rc.user, rc.pass)

	resp, err := rc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("запрос %s не выполнен: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, restErrorMessage(body, resp.StatusCode))
	}

	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("некорректный JSON в ответе %s: %w", path, err)
	}
	return out, nil
}

// restErrorMessage извлекает сообщение об ошибке из тела ответа REST.
func restErrorMessage(body []byte, status int) string {
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Message != "" {
		return e.Message
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return "нет тела ответа"
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

// jstr извлекает строковое значение из JSON-записи (строка или число).
func jstr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// collectREST собирает информацию через REST API.
func collectREST(ctx context.Context, p *Params) (*Info, error) {
	rc, err := newRESTClient(p)
	if err != nil {
		return nil, err
	}

	// Первый запрос проверяет доступность и учётные данные.
	ident, err := rc.get(ctx, "/rest/system/identity")
	if err != nil {
		return nil, err
	}
	info := &Info{Protocol: "rest", Packages: []Package{}}
	if len(ident) > 0 {
		info.Identity = jstr(ident[0], "identity")
	}

	res, err := rc.get(ctx, "/rest/system/resource")
	if err != nil {
		return nil, err
	}
	if len(res) > 0 {
		m := res[0]
		info.Model = jstr(m, "board-name")
		info.Architecture = jstr(m, "architecture-name")
		info.RouterOS = jstr(m, "version")
		info.Uptime = jstr(m, "uptime")
		info.CPU = jstr(m, "cpu")
	}

	// /rest/system/routerboard есть не на всех устройствах — ошибку игнорируем.
	if rb, err := rc.get(ctx, "/rest/system/routerboard"); err == nil && len(rb) > 0 {
		m := rb[0]
		info.Board = jstr(m, "board")
		info.CurrentFirmware = jstr(m, "current-firmware")
		if info.CurrentFirmware == "" {
			info.CurrentFirmware = jstr(m, "firmware")
		}
		info.FactoryFirmware = jstr(m, "factory-firmware")
		info.UpgradeFirmware = jstr(m, "upgrade-firmware")
	}

	pkgs, err := rc.get(ctx, "/rest/system/package")
	if err != nil {
		return nil, err
	}
	for _, m := range pkgs {
		info.Packages = append(info.Packages, Package{
			Name:    jstr(m, "name"),
			Version: jstr(m, "version"),
			Status:  jstr(m, "status"),
		})
	}

	if info.Model == "" {
		info.Model = info.Board
	}
	return info, nil
}
