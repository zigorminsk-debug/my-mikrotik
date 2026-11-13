package mikrotik

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// PPPRadiusSettings configures a RADIUS client for PPP server authentication.
// RouterOS /ppp aaa is GLOBAL to PPP, not just the selected VPN protocol.
type PPPRadiusSettings struct {
	Enabled            bool   `json:"enabled"`
	Address            string `json:"address"`
	AuthenticationPort string `json:"authenticationPort"`
	AccountingPort     string `json:"accountingPort"`
	Secret             string `json:"secret"`
	Acknowledged       bool   `json:"acknowledged"`
}

var radiusSecretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func validatePPPRadius(r PPPRadiusSettings, s *Settings) []string {
	var errs []string
	if s.WAN.Enabled || s.PortForward.Enabled || s.Backup.Enabled || s.GeoNAT.Enabled || s.Security.Enabled || s.LAN.Enabled || s.DHCP.Enabled || s.DNS.Enabled || s.NTP.Enabled || s.PPPoE.Enabled || s.WiFi.Enabled || s.WGServer.Enabled || s.WGClient.Enabled || s.OpenVPN.Enabled || s.PPTP.Enabled || s.OVPNCertificates.Enabled {
		errs = append(errs, "RADIUS PPP: выключите остальные разделы; этот сценарий отдельный")
	}
	ip := net.ParseIP(r.Address)
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || r.Address == "255.255.255.255" {
		errs = append(errs, "RADIUS PPP: укажите IPv4-адрес сервера FreeRADIUS (не localhost)")
	}
	if !validPort(r.AuthenticationPort) || !validPort(r.AccountingPort) {
		errs = append(errs, "RADIUS PPP: укажите корректные UDP-порты аутентификации и accounting")
	}
	if !radiusSecretPattern.MatchString(r.Secret) {
		errs = append(errs, "RADIUS PPP: общий секрет — 16–128 латинских букв, цифр, _ или -")
	}
	if !r.Acknowledged {
		errs = append(errs, "RADIUS PPP: подтвердите глобальное влияние PPP и резервную копию")
	}
	return errs
}

func writePPPRadius(b *strings.Builder, r PPPRadiusSettings) {
	section(b, "RADIUS", "PPP VPN через FreeRADIUS / Active Directory")
	b.WriteString("# MTI-PPP-RADIUS-GUARDED-APPLY\n")
	b.WriteString("# Автоприменение через приложение только после проверки; текст исполняемый при ручном импорте.\n")
	b.WriteString("# ВНИМАНИЕ: use-radius влияет на ВСЕ серверы PPP, в том числе PPPoE, не только VPN.\n")
	b.WriteString("# Локальные PPP secrets проверяются раньше RADIUS. Убедитесь в политике FreeRADIUS, доступности и резервном доступе.\n")
	b.WriteString("# Общий секрет виден в скрипте и на роутере. Роутер и FreeRADIUS должны обмениваться по защищённой доверенной сети.\n")
	b.WriteString("# Защита от дублирования: при любой существующей записи /radius выполнение остановится до изменений.\n")
	b.WriteString(":if ([:len [/radius find]] > 0) do={ :error \"RADIUS entries exist; stop and review manually\" }\n")
	fmt.Fprintf(b, "/radius add service=ppp %s %s %s %s comment=mti-ppp-radius disabled=no\n", attr("address", r.Address), attr("authentication-port", r.AuthenticationPort), attr("accounting-port", r.AccountingPort), attr("secret", r.Secret))
	b.WriteString("/ppp aaa set use-radius=yes\n")
	b.WriteString("# VPN-сервер, FreeRADIUS, AD/LDAP, group policy, сетевой доступ и accounting создаются отдельно.\n")
	b.WriteString("# Если выполнение прервано, изменения могут быть частичными: проверить /radius и /ppp aaa до повторного запуска.\n")
}
