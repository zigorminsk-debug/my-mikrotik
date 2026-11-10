package mikrotik

import (
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// WireGuardSettings describes one peer on a new RouterOS WireGuard interface.
type WireGuardSettings struct {
	Enabled      bool   `json:"enabled"`
	Step         string `json:"step"` // "interface" (шаг 1) или "peer" (шаг 2)
	PrivateKey   string `json:"privateKey"`
	PublicKey    string `json:"publicKey"`
	Address      string `json:"address"`
	AllowedIPs   string `json:"allowedIPs"`
	ListenPort   string `json:"listenPort"`
	Endpoint     string `json:"endpoint"`
	EndpointPort string `json:"endpointPort"`
}

type VPNClientSettings struct {
	Enabled     bool   `json:"enabled"`
	Server      string `json:"server"`
	Port        string `json:"port"`
	Protocol    string `json:"protocol"`
	ExitViaVPN  bool   `json:"exitViaVPN"`
	User        string `json:"user"`
	Password    string `json:"password"`
	Certificate string `json:"certificate"`
}

func validPort(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535
}
func validWGKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}
func validCIDRs(s string) bool {
	parts := splitList(s)
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if _, _, err := net.ParseCIDR(p); err != nil {
			return false
		}
	}
	return true
}
func validHost(s string) bool {
	return net.ParseIP(s) != nil || (len(s) <= 253 && hostRe.MatchString(s))
}
// wgStep возвращает шаг настройки WireGuard. Шаг 1 поднимает интерфейс
// (приватный ключ генерирует сам RouterOS), шаг 2 добавляет peer.
func wgStep(w WireGuardSettings) string {
	if strings.EqualFold(w.Step, "peer") {
		return "peer"
	}
	return "interface"
}

// validateWG проверяет только поля текущего шага: приватный ключ не
// запрашивается никогда, публичный ключ другой стороны нужен на шаге 2.
func validateWG(w WireGuardSettings, client bool) []string {
	var e []string
	if wgStep(w) == "interface" {
		if _, _, err := net.ParseCIDR(w.Address); err != nil {
			e = append(e, "WireGuard: укажите адрес интерфейса с маской")
		}
		if !validPort(w.ListenPort) {
			e = append(e, "WireGuard: некорректный локальный UDP-порт")
		}
		return e
	}
	if !validWGKey(w.PublicKey) {
		e = append(e, "WireGuard: укажите публичный ключ другой стороны (base64, 32 байта)")
	}
	if !validCIDRs(w.AllowedIPs) {
		e = append(e, "WireGuard: укажите допустимые сети peer в формате CIDR")
	}
	if client && (!validHost(w.Endpoint) || !validPort(w.EndpointPort)) {
		e = append(e, "WireGuard клиент: укажите адрес и UDP-порт сервера")
	}
	return e
}
func validateVPNClient(v VPNClientSettings, label string, ovpn bool) []string {
	var e []string
	if !validHost(v.Server) {
		e = append(e, label+": укажите корректный адрес сервера")
	}
	if v.User == "" || v.Password == "" {
		e = append(e, label+": укажите логин и пароль")
	}
	if ovpn && v.Protocol != "" && v.Protocol != "tcp" && v.Protocol != "udp" {
		e = append(e, label+": протокол должен быть TCP или UDP")
	}
	if ovpn && !validPort(v.Port) {
		e = append(e, label+": некорректный порт")
	}
	return e
}
// writeWG пишет скрипт текущего шага настройки WireGuard: шаг 1 поднимает
// интерфейс (ключи генерирует RouterOS), шаг 2 добавляет peer.
func writeWG(b *strings.Builder, w WireGuardSettings, client bool) {
	name, role := "mti-wg-server", "сервер"
	if client {
		name, role = "mti-wg-client", "клиент"
	}
	if wgStep(w) == "peer" {
		writeWGPeer(b, w, name, role, client)
		return
	}
	writeWGInterface(b, w, name, role)
}

// wgPublicKey — выражение, которое читает публичный ключ интерфейса.
func wgPublicKey(name string) string {
	return "[/interface wireguard get [find name=" + rosQuote(name) + "] public-key]"
}

// writeWGInterface — шаг 1: интерфейс и адрес туннеля. Приватный ключ
// не передаётся: его создаёт сам RouterOS, а публичный ключ печатается и
// сохраняется в комментарии интерфейса — оттуда его можно скопировать
// (в том числе из списка интерфейсов в приложении).
func writeWGInterface(b *strings.Builder, w WireGuardSettings, name, role string) {
	section(b, "Interface", "WireGuard — "+role+": шаг 1, интерфейс")
	b.WriteString("# Шаг 1: приватный ключ создаёт сам RouterOS — вводить его не нужно.\n")
	b.WriteString("# Публичный ключ появится в выводе скрипта и в комментарии интерфейса.\n")
	b.WriteString("# Передайте его другой стороне, затем вернитесь и выполните шаг 2.\n")
	create := "/interface wireguard add " + attr("name", name) + " " + attr("listen-port", w.ListenPort)
	// Повторное применение не должно ломать уже созданный интерфейс и его ключи.
	fmt.Fprintf(b, ":if ([:len [/interface wireguard find where name=%s]] = 0) do={ %s } else={ :put %s }\n",
		rosQuote(name), create, rosQuote("WireGuard: интерфейс "+name+" уже есть, ключ не менялся"))
	fmt.Fprintf(b, ":if ([:len [/ip address find where interface=%s]] = 0) do={ /ip address add %s %s comment=%s }\n",
		rosQuote(name), attr("address", w.Address), attr("interface", name), rosQuote(name))
	key := wgPublicKey(name)
	fmt.Fprintf(b, "/interface wireguard set [find name=%s] comment=(\"mti-wg pubkey=\" . %s)\n", rosQuote(name), key)
	fmt.Fprintf(b, ":put (\"WireGuard "+name+" public-key: \" . %s)\n", key)
	b.WriteString("# Интерфейс поднят. На шаге 2 укажите публичный ключ другой стороны, адрес сервера и сети туннеля.\n")
	b.WriteString("# Firewall и маршруты к удалённым сетям настраиваются вручную на обеих сторонах.\n")
}

// writeWGPeer — шаг 2: peer на интерфейсе, созданном на шаге 1. Интерфейс,
// порт и адрес не вводятся повторно — они берутся из настроек шага 1.
func writeWGPeer(b *strings.Builder, w WireGuardSettings, name, role string, client bool) {
	section(b, "Interface", "WireGuard — "+role+": шаг 2, peer")
	b.WriteString("# Шаг 2: peer добавляется к интерфейсу, созданному на шаге 1.\n")
	peer := []string{attr("interface", name), attr("public-key", w.PublicKey),
		attr("allowed-address", strings.Join(splitList(w.AllowedIPs), ","))}
	if client {
		peer = append(peer, attr("endpoint-address", w.Endpoint), attr("endpoint-port", w.EndpointPort), "persistent-keepalive=25s")
	}
	fmt.Fprintf(b, ":if ([:len [/interface wireguard find where name=%s]] = 0) do={ :put %s } else={ /interface wireguard peers add %s }\n",
		rosQuote(name), rosQuote("WireGuard: сначала выполните шаг 1 — интерфейс "+name+" не найден"), strings.Join(peer, " "))
	b.WriteString("# Проверьте handshake: /interface wireguard peers print detail\n")
	b.WriteString("# Firewall и маршруты к удалённым сетям настраиваются вручную на обеих сторонах.\n")
}
func writeVPNClient(b *strings.Builder, v VPNClientSettings, ovpn bool) {
	kind, name := "pptp", "mti-pptp-client"
	if ovpn {
		kind, name = "ovpn", "mti-ovpn-client"
	}
	section(b, "PPP", strings.ToUpper(kind)+" Client")
	b.WriteString("# Создаётся новое подключение; при повторном применении удалите его вручную.\n")
	attrs := []string{attr("name", name), attr("connect-to", v.Server), attr("user", v.User), attr("password", v.Password), "add-default-route=no", "disabled=no"}
	if ovpn && v.ExitViaVPN {
		attrs[4] = "add-default-route=yes"
	}
	if ovpn {
		protocol := v.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		attrs = append(attrs, attr("port", v.Port), attr("protocol", protocol), "route-nopull=yes", "mode=ip", "auth=sha1", "cipher=aes256-cbc", "verify-server-certificate=yes")
		if v.Certificate != "" {
			attrs = append(attrs, attr("certificate", v.Certificate))
		}
	} else {
		attrs = append(attrs, "profile=default-encryption")
	}
	fmt.Fprintf(b, "/interface %s-client add %s\n", kind, strings.Join(attrs, " "))
	if ovpn {
		b.WriteString("# Маршруты, firewall и DNS настраиваются отдельно; route-nopull отключает маршруты от сервера.\n")
		if v.ExitViaVPN {
			b.WriteString("# Проверьте приоритет маршрутов: равные distance могут распределять трафик между WAN и VPN. При обрыве возможен выход через WAN.\n")
		}
	} else {
		b.WriteString("# Маршруты и правила firewall настраиваются отдельно.\n")
	}
}

// OVPNCertificateSettings provisions a new CA and one server/client certificate.
type OVPNCertificateSettings struct {
	Enabled        bool   `json:"enabled"`
	ServerName     string `json:"serverName"`
	ClientName     string `json:"clientName"`
	ExportPassword string `json:"exportPassword"`
}

func validateOVPNCertificates(c OVPNCertificateSettings) []string {
	var errs []string
	if !validHost(c.ServerName) {
		errs = append(errs, "Сертификаты OpenVPN: укажите DNS-имя или IP сервера")
	}
	if len(c.ClientName) > 64 || !safeNameRe.MatchString(c.ClientName) {
		errs = append(errs, "Сертификаты OpenVPN: имя клиента — от 1 до 64 латинских букв, цифр, _ или -")
	}
	if len(c.ExportPassword) < 12 {
		errs = append(errs, "Сертификаты OpenVPN: пароль экспорта должен содержать не менее 12 байт")
	}
	return errs
}

func writeOVPNCertificates(b *strings.Builder, c OVPNCertificateSettings) {
	section(b, "System", "OpenVPN — выпуск нового CA и сертификатов")
	b.WriteString("# Выполняйте на серверном MikroTik; точное время должно быть установлено заранее.\n")
	b.WriteString("# Новый CA не совместим с ранее выпущенными сертификатами. Существующие сертификаты не удаляются.\n")
	b.WriteString(":if ([:len [/certificate find where name=\"mti-ovpn-ca\"]] > 0 || [:len [/certificate find where name=\"mti-ovpn-server\"]] > 0 || [:len [/certificate find where name=\"mti-ovpn-client\"]] > 0) do={ :error \"mti OpenVPN certificates already exist; stop\" }\n")
	b.WriteString("/certificate add name=mti-ovpn-ca common-name=mti-ovpn-ca key-size=2048 digest-algorithm=sha256 days-valid=3650 key-usage=key-cert-sign,crl-sign\n")
	b.WriteString("/certificate sign mti-ovpn-ca\n")
	san := "DNS:" + c.ServerName
	if net.ParseIP(c.ServerName) != nil {
		san = "IP:" + c.ServerName
	}
	fmt.Fprintf(b, "/certificate add name=mti-ovpn-server %s %s key-size=2048 digest-algorithm=sha256 days-valid=825 key-usage=digital-signature,key-encipherment,tls-server\n", attr("common-name", c.ServerName), attr("subject-alt-name", san))
	b.WriteString("/certificate sign mti-ovpn-server ca=mti-ovpn-ca\n")
	fmt.Fprintf(b, "/certificate add name=mti-ovpn-client %s key-size=2048 digest-algorithm=sha256 days-valid=825 key-usage=digital-signature,key-encipherment,tls-client\n", attr("common-name", c.ClientName))
	b.WriteString("/certificate sign mti-ovpn-client ca=mti-ovpn-ca\n")
	b.WriteString("/certificate export-certificate mti-ovpn-ca file-name=mti-ovpn-ca type=pem\n")
	fmt.Fprintf(b, "/certificate export-certificate mti-ovpn-client file-name=mti-ovpn-client type=pkcs12 %s\n", attr("export-passphrase", c.ExportPassword))
	b.WriteString("# В Files: CA (.crt) и клиентский PKCS12 (.p12). Передайте их клиенту защищённо.\n")
	b.WriteString("# Импортируйте CA и PKCS12 на клиенте; используйте имя импортированного клиентского сертификата.\n")
	b.WriteString("# Сервер OpenVPN, PPP-пользователи, маршруты и firewall настраиваются отдельно.\n")
}
