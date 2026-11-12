package mikrotik

import (
	"context"
	"crypto/md5" //nolint:gosec // MD5 используется протоколом RouterOS, не для защиты данных
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"time"
)

const (
	apiDialTimeout = 5 * time.Second
	apiOpTimeout   = 10 * time.Second
)

// routerTLSConfig — TLS-конфигурация для устройств RouterOS с самоподписанным
// сертификатом. MinVersion понижен до TLS 1.0 ради совместимости со старыми
// версиями RouterOS (соединение идёт в локальной сети администратора).
func routerTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // самоподписанный сертификат роутера
		MinVersion:         tls.VersionTLS10,
	}
}

// apiClient — синхронный клиент RouterOS API (бинарный протокол).
type apiClient struct {
	conn net.Conn
	ctx  context.Context
}

func dialAPI(ctx context.Context, p *Params) (*apiClient, error) {
	addr := net.JoinHostPort(p.Host, strconv.Itoa(apiPort(p)))
	dialer := &net.Dialer{Timeout: apiDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("не удалось подключиться к %s: %w", addr, err)
	}
	if p.TLS {
		tconn := tls.Client(conn, routerTLSConfig())
		if err := tconn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("TLS-соединение с %s не установлено: %w", addr, err)
		}
		conn = tconn
	}
	return &apiClient{conn: conn, ctx: ctx}, nil
}

func (c *apiClient) Close() error {
	return c.conn.Close()
}

// exec отправляет одно предложение и читает ответ до !done.
//
// Ответ может содержать !re (записи), !trap (ошибка команды — читаем до !done,
// чтобы не рассинхронизировать поток), !fatal (соединение закрывается),
// !empty (нет записей).
func (c *apiClient) exec(words ...string) (records []map[string]string, done map[string]string, err error) {
	deadline := time.Now().Add(apiOpTimeout)
	if dl, ok := c.ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = c.conn.SetDeadline(deadline)

	if err := writeSentence(c.conn, words...); err != nil {
		return nil, nil, fmt.Errorf("ошибка отправки команды: %w", err)
	}

	records = []map[string]string{}
	var trapMsg string
	for {
		s, err := readSentence(c.conn)
		if err != nil {
			if trapMsg != "" {
				return nil, nil, fmt.Errorf("%s", trapMsg)
			}
			return nil, nil, fmt.Errorf("ошибка чтения ответа: %w", err)
		}
		if len(s) == 0 {
			continue // пустое предложение игнорируем
		}
		switch s[0] {
		case "!done":
			if trapMsg != "" {
				// Команда завершилась ошибкой — сообщаем её, но поток уже синхронизирован.
				return nil, nil, fmt.Errorf("%s", trapMsg)
			}
			return records, attrs(s[1:]), nil
		case "!re":
			records = append(records, attrs(s[1:]))
		case "!trap":
			if trapMsg == "" {
				trapMsg = trapMessage(s)
			}
		case "!fatal":
			return nil, nil, fmt.Errorf("фатальная ошибка роутера: %s", trapMessage(s))
		case "!empty":
			// нет записей — сервер пришлёт !done следующим
		default:
			// Неизвестные предложения не прерывают чтение ответа.
		}
	}
}

func trapMessage(s []string) string {
	m := attrs(s[1:])
	if msg := m["message"]; msg != "" {
		return msg
	}
	return fmt.Sprintf("%s", s[0])
}

// login выполняет вход: сначала пробуется одностадийный вход (RouterOS ≥ 6.43),
// при неудаче — классический двухэтапный вход с challenge (RouterOS < 6.43).
func (c *apiClient) login(user, password string) error {
	records, done, err := c.exec("/login", "=name="+user, "=password="+password)
	_ = records
	if err == nil {
		if ch, ok := done["ret"]; ok && ch != "" {
			return c.challengeLogin(user, password, ch)
		}
		return nil
	}

	// Похоже, это старый RouterOS: запрашиваем challenge отдельно.
	_, done2, err2 := c.exec("/login")
	if err2 == nil {
		if ch, ok := done2["ret"]; ok && ch != "" {
			return c.challengeLogin(user, password, ch)
		}
		return fmt.Errorf("не удалось авторизоваться: сервер не поддерживает ожидаемый вход")
	}
	return fmt.Errorf("не удалось авторизоваться: %v; запасной вход: %v", err, err2)
}

// challengeLogin выполняет второй этап входа по схеме RouterOS < 6.43:
// response = "00" + md5(0x00 || пароль || challenge).
func (c *apiClient) challengeLogin(user, password, challengeHex string) error {
	response, err := challengeResponse(challengeHex, password)
	if err != nil {
		return err
	}
	if _, _, err := c.exec("/login", "=name="+user, "=response="+response); err != nil {
		return fmt.Errorf("авторизация по challenge не прошла: %w", err)
	}
	return nil
}

// challengeResponse считает ответ на challenge по алгоритму RouterOS.
func challengeResponse(challengeHex, password string) (string, error) {
	ch, err := hex.DecodeString(challengeHex)
	if err != nil {
		return "", fmt.Errorf("некорректный challenge от сервера: %w", err)
	}
	h := md5.New() //nolint:gosec // протокол RouterOS требует MD5
	h.Write([]byte{0x00})
	h.Write([]byte(password))
	h.Write(ch)
	return "00" + hex.EncodeToString(h.Sum(nil)), nil
}

// collectAPI собирает информацию через RouterOS API.
func collectAPI(ctx context.Context, p *Params) (*Info, error) {
	c, err := dialAPI(ctx, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()

	if err := c.login(p.User, p.Password); err != nil {
		return nil, err
	}

	info := &Info{Protocol: "api", Packages: []Package{}}

	recs, _, err := c.exec("/system/identity/print")
	if err != nil {
		return nil, fmt.Errorf("чтение /system/identity: %w", err)
	}
	if len(recs) > 0 {
		info.Identity = recs[0]["name"]
		if info.Identity == "" {
			info.Identity = recs[0]["identity"]
		}
	}

	recs, _, err = c.exec("/system/resource/print")
	if err != nil {
		return nil, fmt.Errorf("чтение /system/resource: %w", err)
	}
	if len(recs) == 0 || recs[0]["version"] == "" {
		return nil, fmt.Errorf("не удалось прочитать версию RouterOS из /system/resource")
	}
	if len(recs) > 0 {
		m := recs[0]
		info.Model = m["board-name"]
		info.Architecture = m["architecture-name"]
		info.RouterOS = m["version"]
		info.Uptime = m["uptime"]
		info.CPU = m["cpu"]
	}

	// /system/routerboard/print есть не на всех устройствах (CHR, x86) —
	// ошибку здесь игнорируем.
	if recs, _, err := c.exec("/system/routerboard/print"); err == nil && len(recs) > 0 {
		m := recs[0]
		info.Board = m["model"]
		if info.Board == "" {
			info.Board = m["board"] // compatibility with older/custom replies
		}
		info.CurrentFirmware = m["current-firmware"]
		if info.CurrentFirmware == "" {
			info.CurrentFirmware = m["firmware"]
		}
		info.FactoryFirmware = m["factory-firmware"]
		info.UpgradeFirmware = m["upgrade-firmware"]
	}

	recs, _, err = c.exec("/system/package/print")
	if err != nil {
		return nil, fmt.Errorf("чтение /system/package: %w", err)
	}
	for _, m := range recs {
		info.Packages = append(info.Packages, Package{
			Name:     m["name"],
			Version:  m["version"],
			Status:   m["status"],
			Disabled: toBool(m["disabled"]),
		})
	}

	if info.Model == "" {
		info.Model = info.Board
	}
	return info, nil
}
