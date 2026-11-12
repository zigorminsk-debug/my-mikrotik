package mikrotik

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Порты RouterOS по умолчанию.
const (
	DefaultAPIPort    = 8728 // RouterOS API (TCP)
	DefaultAPIPortTLS = 8729 // RouterOS API поверх TLS (TCP)
	DefaultRESTPort   = 80   // REST API (HTTP)
	DefaultRESTPortT  = 443  // REST API поверх HTTPS
)

// Package — установленный пакет RouterOS.
type Package struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Status   string `json:"status,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// Info — информация, полученная с роутера.
type Info struct {
	Protocol        string    `json:"protocol"` // "api" или "rest"
	Identity        string    `json:"identity,omitempty"`
	Model           string    `json:"model,omitempty"`
	Board           string    `json:"board,omitempty"`
	Architecture    string    `json:"architecture,omitempty"`
	RouterOS        string    `json:"routeros,omitempty"`
	CurrentFirmware string    `json:"currentFirmware,omitempty"`
	FactoryFirmware string    `json:"factoryFirmware,omitempty"`
	UpgradeFirmware string    `json:"upgradeFirmware,omitempty"`
	Uptime          string    `json:"uptime,omitempty"`
	CPU             string    `json:"cpu,omitempty"`
	Packages        []Package `json:"packages"`
}

// Params — параметры подключения к роутеру.
type Params struct {
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`     // 0 = порт по умолчанию для протокола
	APIPort  int    `json:"apiPort,omitempty"`  // явный порт RouterOS API (в основном для тестов)
	RESTPort int    `json:"restPort,omitempty"` // явный порт REST (в основном для тестов)
	User     string `json:"user"`
	Password string `json:"password"`
	TLS      bool   `json:"tls"`
	Protocol string `json:"protocol"` // "auto" | "api" | "rest"
}

// normalize приводит параметры к каноническому виду и проверяет их.
func (p *Params) normalize() error {
	h := strings.TrimSpace(p.Host)
	// Отбрасываем схему и путь, если пользователь ввёл URL.
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	// MAC-адрес (подключение по MAC без IP) не разбираем как host:port:
	// иначе последний октет MAC мог бы съесться как номер порта.
	isMAC := false
	if _, err := net.ParseMAC(h); err == nil {
		isMAC = true
	} else {
		// host:port в поле адреса → разбираем, порт из поля адреса имеет приоритет,
		// только если отдельное поле порта не заполнено.
		if host, portStr, err := net.SplitHostPort(h); err == nil {
			if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
				if p.Port == 0 {
					p.Port = port
				}
				h = host
			}
		}
		// IPv6 в квадратных скобках без порта.
		if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
			h = h[1 : len(h)-1]
		}
	}
	p.Host = h
	p.User = strings.TrimSpace(p.User)

	if p.Host == "" {
		return fmt.Errorf("не указан адрес роутера")
	}
	if p.User == "" {
		return fmt.Errorf("не указан логин")
	}

	// MAC-адрес всегда означает подключение по MAC (MAC-Telnet).
	if isMAC {
		p.Protocol = "mac"
	} else if p.Protocol == "mac" {
		return fmt.Errorf("протокол mac требует MAC-адрес роутера")
	}

	switch p.Protocol {
	case "":
		p.Protocol = "auto"
	case "auto", "api", "rest", "mac":
	default:
		return fmt.Errorf("неизвестный протокол %q (ожидается auto, api, rest или mac)", p.Protocol)
	}
	return nil
}

// apiPort возвращает TCP-порт RouterOS API с учётом приоритета полей.
func apiPort(p *Params) int {
	switch {
	case p.APIPort > 0:
		return p.APIPort
	case p.Port > 0:
		return p.Port
	case p.TLS:
		return DefaultAPIPortTLS
	default:
		return DefaultAPIPort
	}
}

// restPort возвращает порт REST API с учётом приоритета полей.
func restPort(p *Params) int {
	switch {
	case p.RESTPort > 0:
		return p.RESTPort
	case p.Port > 0:
		return p.Port
	case p.TLS:
		return DefaultRESTPortT
	default:
		return DefaultRESTPort
	}
}

// Collect подключается к роутеру и собирает информацию.
//
// При Protocol="auto" сначала пробуется RouterOS API, затем REST API;
// возвращается результат первого успешного варианта, при неудаче —
// объединённое описание ошибок обоих протоколов. Если в Host указан
// MAC-адрес, подключение идёт по MAC (MAC-Telnet).
func Collect(ctx context.Context, p Params) (*Info, error) {
	if err := p.normalize(); err != nil {
		return nil, err
	}
	if p.Protocol == "mac" {
		return collectMAC(ctx, &p)
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
		if err := ctx.Err(); err != nil {
			errs = append(errs, err.Error())
			break
		}
		var (
			info *Info
			err  error
		)
		switch proto {
		case "api":
			info, err = collectAPI(ctx, &p)
		case "rest":
			info, err = collectREST(ctx, &p)
		}
		if err == nil {
			return info, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %s", proto, err))
	}
	return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
}
