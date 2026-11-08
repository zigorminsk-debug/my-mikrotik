package mikrotik

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Настройки конструктора (JSON из веб-интерфейса)
// ---------------------------------------------------------------------------

// LanSettings — IP → Addresses.
type LanSettings struct {
	Enabled   bool   `json:"enabled"`
	Interface string `json:"iface"`
	Address   string `json:"address"` // CIDR, например 192.168.88.1/24
}

// DHCPSettings — IP → DHCP Server.
type DHCPSettings struct {
	Enabled   bool   `json:"enabled"`
	Interface string `json:"iface"` // пусто = интерфейс LAN
	PoolStart string `json:"poolStart"`
	PoolEnd   string `json:"poolEnd"`
	LeaseTime string `json:"leaseTime"` // 10m, 1h, 1d, 3d…
}

// DNSSettings — IP → DNS.
type DNSSettings struct {
	Enabled     bool   `json:"enabled"`
	Servers     string `json:"servers"` // через запятую
	AllowRemote bool   `json:"allowRemote"`
}

// NTPSettings — System → NTP Client.
type NTPSettings struct {
	Enabled  bool   `json:"enabled"`
	Servers  string `json:"servers"`
	TimeZone string `json:"timezone"`
}

// PPPoESettings — PPP → PPPoE Client (интернет).
type PPPoESettings struct {
	Enabled      bool   `json:"enabled"`
	Interface    string `json:"iface"`
	Name         string `json:"name"`
	User         string `json:"user"`
	Password     string `json:"password"`
	ServiceName  string `json:"serviceName"`
	DefaultRoute bool   `json:"defaultRoute"`
	UsePeerDNS   bool   `json:"usePeerDNS"`
	NAT          bool   `json:"nat"`
}

// WiFiSettings — Interface → Wireless.
type WiFiSettings struct {
	Enabled    bool   `json:"enabled"`
	Interface  string `json:"iface"`
	SSID       string `json:"ssid"`
	Passphrase string `json:"passphrase"`
	Country    string `json:"country"`
}

// Settings — все настройки конструктора.
type Settings struct {
	LAN   LanSettings   `json:"lan"`
	DHCP  DHCPSettings  `json:"dhcp"`
	DNS   DNSSettings   `json:"dns"`
	NTP   NTPSettings   `json:"ntp"`
	PPPoE PPPoESettings `json:"pppoe"`
	WiFi  WiFiSettings  `json:"wifi"`
}

// ConfigRequest — запрос на генерацию конфигурации.
type ConfigRequest struct {
	Device     *Info    `json:"device"`
	Interfaces []Iface  `json:"interfaces"`
	Settings   Settings `json:"settings"`
}

// ConfigResult — результат генерации.
type ConfigResult struct {
	OK       bool          `json:"ok"`
	Script   string        `json:"script,omitempty"`
	Error    string        `json:"error,omitempty"`
	Features []FeatureInfo `json:"features,omitempty"`
	Derived  *Derived      `json:"derived,omitempty"`
	Skipped  []SkippedInfo `json:"skipped,omitempty"`
}

// FeatureInfo — доступность раздела конструктора на этом устройстве.
type FeatureInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Path      string `json:"path"`              // путь в меню RouterOS
	Package   string `json:"package,omitempty"` // требуемый пакет
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"` // почему недоступен
	Mode      string `json:"mode,omitempty"`   // wifi: legacy | wave2
}

// SkippedInfo — включённый раздел, который пропущен из-за недоступности.
type SkippedInfo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// Derived — вычисленные по подсети значения (для подсказок в интерфейсе).
type Derived struct {
	Pool      string `json:"pool"`      // 192.168.88.2-192.168.88.254
	PoolStart string `json:"poolStart"` // начало одиночного диапазона (если применимо)
	PoolEnd   string `json:"poolEnd"`
	Network   string `json:"network"` // 192.168.88.0/24
	Gateway   string `json:"gateway"` // 192.168.88.1
}

// ---------------------------------------------------------------------------
// Пакеты и доступность разделов
// ---------------------------------------------------------------------------

const (
	featLAN   = "lan"
	featDHCP  = "dhcp"
	featDNS   = "dns"
	featNTP   = "ntp"
	featPPPoE = "pppoe"
	featWiFi  = "wifi"
)

func packageSet(device *Info) map[string]bool {
	set := map[string]bool{}
	if device == nil {
		return set
	}
	for _, p := range device.Packages {
		set[strings.ToLower(strings.TrimSpace(p.Name))] = true
	}
	return set
}

// isCombinedPackage сообщает, что установлен объединённый пакет
// (routeros, routeros-arm, routeros-mmips …), внутри которого есть
// system, dhcp, ppp, wireless и остальные «классические» компоненты.
func isCombinedPackage(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "routeros" || strings.HasPrefix(name, "routeros-")
}

// hasWiFiPackage: пакет с поддержкой беспроводных интерфейсов.
// RouterOS 6 — wireless; RouterOS 7 — wifiwave2 / wifi-qcom / wifi-qcom-ac / wifi;
// объединённый routeros содержит поддержку для моделей с радио.
func hasWiFiPackage(pkgs map[string]bool, combined bool) (bool, string) {
	if combined {
		return true, "routeros"
	}
	for _, name := range []string{"wifiwave2", "wifi-qcom-ac", "wifi-qcom", "wifi", "wireless"} {
		if pkgs[name] {
			return true, name
		}
	}
	return false, ""
}

func hasAnyIface(ifaces []Iface, pred func(Iface) bool) bool {
	for _, i := range ifaces {
		if pred(i) {
			return true
		}
	}
	return false
}

// IsWiFiIface определяет беспроводной интерфейс по типу или имени.
func IsWiFiIface(i Iface) bool {
	switch strings.ToLower(i.Type) {
	case "wireless", "wifi", "wlan":
		return true
	}
	n := strings.ToLower(i.Name)
	return strings.HasPrefix(n, "wlan") || strings.HasPrefix(n, "wifi")
}

// AnalyzeFeatures возвращает список разделов с признаком доступности
// на конкретном устройстве (по пакетам и интерфейсам).
func AnalyzeFeatures(device *Info, ifaces []Iface) []FeatureInfo {
	pkgs := packageSet(device)

	// Объединённый пакет routeros (типично для RouterOS 7 и комбинированных
	// сборок RouterOS 6) содержит system, dhcp, ppp и поддержку радио.
	combined := false
	for name := range pkgs {
		if isCombinedPackage(name) {
			combined = true
		}
	}
	has := func(pkg string) bool { return pkgs[pkg] || combined }
	wifiPkgOK, _ := hasWiFiPackage(pkgs, combined)
	wifiIface := hasAnyIface(ifaces, IsWiFiIface)

	features := []FeatureInfo{
		{ID: featLAN, Title: "Адреса и подсеть", Path: "/ip address",
			Package: "system", Available: has("system") || device == nil,
			Reason: reasonIf(!has("system") && device != nil, "system")},
		{ID: featDHCP, Title: "DHCP-сервер", Path: "/ip dhcp-server",
			Package: "dhcp", Available: has("dhcp"),
			Reason: reasonIf(!has("dhcp"), "dhcp")},
		{ID: featDNS, Title: "DNS", Path: "/ip dns",
			Package: "system", Available: has("system") || device == nil,
			Reason: reasonIf(!has("system") && device != nil, "system")},
		{ID: featNTP, Title: "NTP-клиент", Path: "/system ntp client",
			Package: "system", Available: has("system") || device == nil,
			Reason: reasonIf(!has("system") && device != nil, "system")},
		{ID: featPPPoE, Title: "PPPoE-клиент (Интернет)", Path: "/interface pppoe-client",
			Package: "ppp", Available: has("ppp"),
			Reason: reasonIf(!has("ppp"), "ppp")},
	}

	wifi := FeatureInfo{
		ID: featWiFi, Title: "Wi-Fi", Path: "/interface wireless",
		Available: wifiPkgOK && wifiIface,
	}
	pkgName := ""
	if wifiPkgOK {
		_, pkgName = hasWiFiPackage(pkgs, combined)
	}
	switch {
	case !wifiPkgOK:
		wifi.Package = "wireless/wifiwave2"
		wifi.Reason = "необходимый пакет отсутствует: wireless/wifiwave2"
	case !wifiIface:
		wifi.Package = pkgName
		wifi.Reason = "на устройстве нет беспроводных интерфейсов"
	default:
		wifi.Package = pkgName
		// Режим выбирается по типу интерфейса, затем по пакету.
		wifi.Mode = wifiModeFor(device, ifaces, pkgName)
	}
	features = append(features, wifi)
	return features
}

func reasonIf(cond bool, pkg string) string {
	if cond {
		return "необходимый пакет отсутствует: " + pkg
	}
	return ""
}

// wifiModeFor: "wave2" (/interface wifi, RouterOS 7 wifiwave2) или "legacy" (/interface wireless).
func wifiModeFor(device *Info, ifaces []Iface, pkg string) string {
	for _, i := range ifaces {
		if !IsWiFiIface(i) {
			continue
		}
		t := strings.ToLower(i.Type)
		if t == "wifi" {
			return "wave2"
		}
		if t == "wireless" {
			return "legacy"
		}
	}
	switch pkg {
	case "wifiwave2", "wifi-qcom", "wifi-qcom-ac", "wifi":
		return "wave2"
	}
	return "legacy"
}

// ---------------------------------------------------------------------------
// Валидация и вычисления по подсети
// ---------------------------------------------------------------------------

var safeNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

func splitList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == ';' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validIPv4(s string) bool {
	ip := net.ParseIP(strings.TrimSpace(s))
	return ip != nil && ip.To4() != nil
}

// deriveLAN вычисляет сеть, шлюз и диапазон(ы) пула из CIDR LAN.
// Пул — все адреса подсети, кроме адреса роутера (поддерживает несколько
// диапазонов через запятую, если адрес роутера стоит в середине).
func deriveLAN(cidr string) (network, gateway, pool string, err error) {
	trimmed := strings.TrimSpace(cidr)
	if trimmed == "" {
		return "", "", "", fmt.Errorf("укажите адрес роутера с маской, например 192.168.88.1/24")
	}
	if !strings.Contains(trimmed, "/") {
		return "", "", "", fmt.Errorf("адрес %q без маски — укажите, например %s/24", trimmed, trimmed)
	}
	ip, ipnet, e := net.ParseCIDR(trimmed)
	if e != nil {
		return "", "", "", fmt.Errorf("некорректный адрес с маской %q", cidr)
	}
	v4 := ip.To4()
	if v4 == nil {
		return "", "", "", fmt.Errorf("ожидается IPv4-адрес, получено %q", cidr)
	}
	_, bits := ipnet.Mask.Size()
	if bits != 32 {
		return "", "", "", fmt.Errorf("ожидается IPv4-маска, получено %q", cidr)
	}
	network = ipnet.String()
	gateway = v4.String()

	// broadcast = network | ^mask
	bcast := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		bcast[i] = ipnet.IP.To4()[i] | ^ipnet.Mask[i]
	}
	start := incIP(ipnet.IP.To4()) // первый usable-адрес (network + 1)
	end := decIP(bcast)            // последний usable
	router := v4
	if cmpIP(start, end) > 0 {
		// Подсеть слишком мала для пула — не ошибка сама по себе,
		// проверка будет при включённом DHCP.
		return network, gateway, "", nil
	}

	var segs []string
	addSeg := func(a, b net.IP) {
		if cmpIP(a, b) > 0 {
			return
		}
		if cmpIP(a, b) == 0 {
			segs = append(segs, a.String())
			return
		}
		segs = append(segs, a.String()+"-"+b.String())
	}

	// Диапазоны [start, end] минус адрес роутера.
	if cmpIP(router, start) < 0 || cmpIP(router, end) > 0 {
		addSeg(start, end)
	} else {
		addSeg(start, decIP(router))
		addSeg(incIP(router), end)
	}
	pool = strings.Join(segs, ",")
	return network, gateway, pool, nil
}

func incIP(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			break
		}
	}
	return out
}

func decIP(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] > 0 {
			out[i]--
			break
		}
		out[i] = 0xFF
	}
	return out
}

func cmpIP(a, b net.IP) int {
	aa, bb := a.To4(), b.To4()
	if aa == nil || bb == nil {
		return strings.Compare(a.String(), b.String())
	}
	for i := 0; i < 4; i++ {
		if aa[i] < bb[i] {
			return -1
		}
		if aa[i] > bb[i] {
			return 1
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// Генерация скрипта
// ---------------------------------------------------------------------------

const scriptCommentTag = "mti-lan" // комментарий-маркер создаваемых объектов

var safeAttrRe = regexp.MustCompile(`^[A-Za-z0-9._:/@,-]*$`)

// rosQuote экранирует значение аргумента для командной строки RouterOS.
func rosQuote(v string) string {
	if v != "" && safeAttrRe.MatchString(v) {
		return v
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func attr(k, v string) string {
	return k + "=" + rosQuote(v)
}

// ---------------------------------------------------------------------------
// Приведение IP-адресов к формату
// ---------------------------------------------------------------------------

// normOctets приводит группу октетов к каноническому виду:
// "192.168.088.001" → "192.168.1.10"? нет: 192.168.88.1; лишние октеты
// отбрасываются, значения больше 255 ограничиваются 255.
func normOctets(s string) string {
	parts := strings.Split(s, ".")
	if len(parts) > 4 {
		parts = parts[:4]
	}
	for i, p := range parts {
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, p)
		if digits == "" {
			parts[i] = ""
			continue
		}
		n, err := strconv.Atoi(digits)
		if err != nil || n > 255 {
			n = 255
		}
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// NormalizeAddr приводит адрес (с маской или без) к формату IP-адреса.
func NormalizeAddr(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	slash := strings.Index(s, "/")
	if slash < 0 {
		return normOctets(s)
	}
	ip := normOctets(s[:slash])
	mask := strings.TrimFunc(s[slash+1:], func(r rune) bool { return r < '0' || r > '9' })
	if mask == "" {
		return ip
	}
	n, err := strconv.Atoi(mask)
	if err != nil || n > 32 {
		n = 32
	}
	return ip + "/" + strconv.Itoa(n)
}

// NormalizeAddrList приводит список адресов через запятую к формату.
func NormalizeAddrList(s string) string {
	parts := splitList(s)
	for i, p := range parts {
		parts[i] = NormalizeAddr(p)
	}
	return strings.Join(parts, ",")
}

// normalizeIPFields правит «сырые» значения из интерфейса: ведущие нули,
// лишние октеты, маску больше /32 — до валидации и генерации скрипта.
func normalizeIPFields(s *Settings) {
	s.LAN.Address = NormalizeAddr(s.LAN.Address)
	s.DHCP.PoolStart = NormalizeAddr(s.DHCP.PoolStart)
	s.DHCP.PoolEnd = NormalizeAddr(s.DHCP.PoolEnd)
	s.DNS.Servers = NormalizeAddrList(s.DNS.Servers)
	// NTP-серверы — это может быть имя, поэтому нормализуем только явные IP.
	parts := splitList(s.NTP.Servers)
	for i, p := range parts {
		if looksLikeIP(p) {
			parts[i] = NormalizeAddr(p)
		}
	}
	s.NTP.Servers = strings.Join(parts, ",")
}

// looksLikeIP: строка из цифр и точек — значит, задумывался адрес.
func looksLikeIP(s string) bool {
	if !strings.Contains(s, ".") {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// Generate собирает скрипт для New Terminal на основе данных об устройстве
// (модель, прошивка, пакеты, интерфейсы) и настроек конструктора.
func Generate(device *Info, ifaces []Iface, s *Settings, now time.Time) *ConfigResult {
	if s == nil {
		s = &Settings{}
	}
	// Копия: правки нормализации не меняют исходный запрос.
	settings := *s
	normalizeIPFields(&settings)
	s = &settings

	features := AnalyzeFeatures(device, ifaces)
	avail := map[string]bool{}
	mode := "legacy"
	for _, f := range features {
		avail[f.ID] = f.Available
		if f.ID == featWiFi && f.Mode != "" {
			mode = f.Mode
		}
	}

	res := &ConfigResult{OK: true, Features: features}
	var errs []string

	// --- вычисления ---
	var (
		lanNetwork, lanGateway, lanPool string
		lanErr                          error
	)
	if s.LAN.Enabled && avail[featLAN] {
		lanNetwork, lanGateway, lanPool, lanErr = deriveLAN(s.LAN.Address)
	}

	res.Derived = &Derived{}
	if lanErr == nil && lanNetwork != "" {
		res.Derived.Network = lanNetwork
		res.Derived.Gateway = lanGateway
		res.Derived.Pool = lanPool
		if i := strings.Index(lanPool, ","); i < 0 {
			if j := strings.Index(lanPool, "-"); j > 0 {
				res.Derived.PoolStart = lanPool[:j]
				res.Derived.PoolEnd = lanPool[j+1:]
			} else {
				res.Derived.PoolStart = lanPool
				res.Derived.PoolEnd = lanPool
			}
		}
	}

	// --- валидация ---
	if s.LAN.Enabled && avail[featLAN] {
		if strings.TrimSpace(s.LAN.Interface) == "" {
			errs = append(errs, "Адреса: выберите интерфейс")
		}
		if lanErr != nil {
			errs = append(errs, "Адреса: "+lanErr.Error())
		}
	}
	if s.DHCP.Enabled && avail[featDHCP] {
		if !s.LAN.Enabled || !avail[featLAN] {
			errs = append(errs, "DHCP: сначала включите раздел «IP → Addresses»")
		}
		if strings.TrimSpace(dhcpIface(s)) == "" {
			errs = append(errs, "DHCP: выберите интерфейс")
		}
		if strings.TrimSpace(s.DHCP.LeaseTime) == "" {
			errs = append(errs, "DHCP: укажите время аренды")
		}
		// Проверки пула — только когда LAN-адрес успешно разобран.
		if lanNetwork != "" {
			if lanErr == nil && lanPool == "" && strings.TrimSpace(s.DHCP.PoolStart) == "" {
				errs = append(errs, "DHCP: подсеть "+lanNetwork+" слишком мала для пула")
			}
			start, end := strings.TrimSpace(s.DHCP.PoolStart), strings.TrimSpace(s.DHCP.PoolEnd)
			if (start == "") != (end == "") {
				errs = append(errs, "DHCP: укажите и начало, и конец пула (или оставьте оба поля пустыми для авто-подбора)")
			} else if start != "" && lanErr == nil {
				if !validIPv4(start) || !validIPv4(end) {
					errs = append(errs, "DHCP: пул должен быть двумя IPv4-адресами")
				} else if !ipInCIDR(start, s.LAN.Address) || !ipInCIDR(end, s.LAN.Address) {
					errs = append(errs, "DHCP: диапазон пула должен лежать внутри подсети "+lanNetwork)
				} else if cmpIPv4(start, end) > 0 {
					errs = append(errs, "DHCP: начало пула больше его конца")
				}
			}
		}
	}
	if s.DNS.Enabled && avail[featDNS] {
		servers := splitList(s.DNS.Servers)
		if len(servers) == 0 {
			errs = append(errs, "DNS: укажите серверы (например 8.8.8.8)")
		}
		for _, srv := range servers {
			if net.ParseIP(srv) == nil {
				errs = append(errs, "DNS: некорректный адрес сервера "+srv)
				break
			}
		}
	}
	if s.NTP.Enabled && avail[featNTP] {
		servers := splitList(s.NTP.Servers)
		if len(servers) == 0 {
			errs = append(errs, "NTP: укажите серверы времени")
		}
		for _, srv := range servers {
			if net.ParseIP(srv) == nil && !hostRe.MatchString(srv) {
				errs = append(errs, "NTP: некорректный сервер "+srv)
				break
			}
		}
		if strings.TrimSpace(s.NTP.TimeZone) == "" {
			errs = append(errs, "NTP: укажите часовой пояс (например Europe/Minsk)")
		}
	}
	if s.PPPoE.Enabled && avail[featPPPoE] {
		if strings.TrimSpace(s.PPPoE.Interface) == "" {
			errs = append(errs, "PPPoE: выберите WAN-интерфейс")
		}
		if !safeNameRe.MatchString(s.PPPoE.Name) {
			errs = append(errs, "PPPoE: имя интерфейса — только латиница, цифры, «_» и «-»")
		}
		if strings.TrimSpace(s.PPPoE.User) == "" {
			errs = append(errs, "PPPoE: укажите логин")
		}
		if s.PPPoE.Password == "" {
			errs = append(errs, "PPPoE: укажите пароль")
		}
	}
	if s.WiFi.Enabled && avail[featWiFi] {
		if strings.TrimSpace(s.WiFi.Interface) == "" {
			errs = append(errs, "Wi-Fi: выберите беспроводной интерфейс")
		}
		ssid := strings.TrimSpace(s.WiFi.SSID)
		if ssid == "" {
			errs = append(errs, "Wi-Fi: укажите имя сети (SSID)")
		} else if len(ssid) > 32 {
			errs = append(errs, "Wi-Fi: SSID длиннее 32 символов")
		}
		if len(s.WiFi.Passphrase) < 8 {
			errs = append(errs, "Wi-Fi: пароль должен содержать не менее 8 символов")
		} else if len(s.WiFi.Passphrase) > 63 {
			errs = append(errs, "Wi-Fi: пароль длиннее 63 символов")
		}
		if strings.TrimSpace(s.WiFi.Country) == "" {
			errs = append(errs, "Wi-Fi: укажите страну (например BY)")
		}
	}

	// Включённые, но недоступные разделы → skipped.
	for _, f := range features {
		enabled := false
		switch f.ID {
		case featLAN:
			enabled = s.LAN.Enabled
		case featDHCP:
			enabled = s.DHCP.Enabled
		case featDNS:
			enabled = s.DNS.Enabled
		case featNTP:
			enabled = s.NTP.Enabled
		case featPPPoE:
			enabled = s.PPPoE.Enabled
		case featWiFi:
			enabled = s.WiFi.Enabled
		}
		if enabled && !f.Available {
			res.Skipped = append(res.Skipped, SkippedInfo{ID: f.ID, Title: f.Title, Reason: f.Reason})
		}
	}

	if len(errs) > 0 {
		res.OK = false
		res.Error = strings.Join(errs, "; ")
		return res
	}

	// --- скрипт ---
	res.Script = buildScript(device, s, now, avail, lanNetwork, lanGateway, lanPool, mode)
	return res
}

func dhcpIface(s *Settings) string {
	if strings.TrimSpace(s.DHCP.Interface) != "" {
		return strings.TrimSpace(s.DHCP.Interface)
	}
	return strings.TrimSpace(s.LAN.Interface)
}

func ipInCIDR(ipStr, cidr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	return ipnet.Contains(ip)
}

func cmpIPv4(a, b string) int {
	ipa, ipb := net.ParseIP(a), net.ParseIP(b)
	if ipa == nil || ipb == nil {
		return strings.Compare(a, b)
	}
	return cmpIP(ipa, ipb)
}

func section(b *strings.Builder, menu, title string) {
	fmt.Fprintf(b, "\n# [%s] %s\n", menu, title)
}

func buildScript(device *Info, s *Settings, now time.Time, avail map[string]bool,
	lanNetwork, lanGateway, lanPool, wifiMode string) string {

	var b strings.Builder
	model := "неизвестная модель"
	if device != nil && device.Model != "" {
		model = device.Model
	}
	rosVer := "unknown"
	if device != nil && device.RouterOS != "" {
		rosVer = device.RouterOS
	}
	fmt.Fprintf(&b, "# ================================================================\n")
	fmt.Fprintf(&b, "#  MikroTik Inspector — конфигурация для %s\n", model)
	fmt.Fprintf(&b, "#  RouterOS %s · %s\n", rosVer, now.UTC().Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(&b, "#  Вставьте в New Terminal и выполните.\n")
	fmt.Fprintf(&b, "# ================================================================\n")

	anyOn := false

	if s.LAN.Enabled && avail[featLAN] {
		anyOn = true
		section(&b, "IP", "Addresses — адреса и подсеть")
		fmt.Fprintf(&b, "/ip address remove [find comment=%s]\n", scriptCommentTag)
		fmt.Fprintf(&b, "/ip address add %s %s comment=%s disabled=no\n",
			attr("address", strings.TrimSpace(s.LAN.Address)),
			attr("interface", strings.TrimSpace(s.LAN.Interface)),
			scriptCommentTag)
	}

	if s.DHCP.Enabled && avail[featDHCP] && s.LAN.Enabled && avail[featLAN] {
		anyOn = true
		section(&b, "IP", "DHCP Server")
		pool := lanPool
		if st, en := strings.TrimSpace(s.DHCP.PoolStart), strings.TrimSpace(s.DHCP.PoolEnd); st != "" && en != "" {
			if st == en {
				pool = st
			} else {
				pool = st + "-" + en
			}
		}
		fmt.Fprintf(&b, "/ip pool remove [find name=mti-pool]\n")
		fmt.Fprintf(&b, "/ip pool add %s %s\n", attr("name", "mti-pool"), attr("ranges", pool))
		fmt.Fprintf(&b, "/ip dhcp-server remove [find name=mti-dhcp]\n")
		fmt.Fprintf(&b, "/ip dhcp-server add %s %s %s %s disabled=no\n",
			attr("name", "mti-dhcp"),
			attr("interface", dhcpIface(s)),
			attr("address-pool", "mti-pool"),
			attr("lease-time", strings.TrimSpace(s.DHCP.LeaseTime)))
		fmt.Fprintf(&b, "/ip dhcp-server-network remove [find comment=mti-net]\n")
		netAttrs := []string{
			attr("address", lanNetwork),
			attr("gateway", lanGateway),
		}
		if s.DNS.Enabled && avail[featDNS] {
			netAttrs = append(netAttrs, attr("dns-server", strings.TrimSpace(s.DNS.Servers)))
		}
		netAttrs = append(netAttrs, "comment=mti-net")
		fmt.Fprintf(&b, "/ip dhcp-server-network add %s\n", strings.Join(netAttrs, " "))
	}

	if s.DNS.Enabled && avail[featDNS] {
		anyOn = true
		section(&b, "IP", "DNS")
		remote := "no"
		if s.DNS.AllowRemote {
			remote = "yes"
		}
		fmt.Fprintf(&b, "/ip dns set %s allow-remote-requests=%s\n",
			attr("servers", strings.TrimSpace(s.DNS.Servers)), remote)
	}

	if s.NTP.Enabled && avail[featNTP] {
		anyOn = true
		section(&b, "System", "NTP Client")
		fmt.Fprintf(&b, "/system ntp client set enabled=yes %s\n",
			attr("servers", strings.TrimSpace(s.NTP.Servers)))
		fmt.Fprintf(&b, "/system clock set %s\n", attr("time-zone-name", strings.TrimSpace(s.NTP.TimeZone)))
	}

	if s.PPPoE.Enabled && avail[featPPPoE] {
		anyOn = true
		section(&b, "PPP", "PPPoE Client — Интернет")
		name := strings.TrimSpace(s.PPPoE.Name)
		fmt.Fprintf(&b, "/interface pppoe-client remove [find name=%s]\n", rosQuote(name))
		attrs := []string{
			attr("name", name),
			attr("interface", strings.TrimSpace(s.PPPoE.Interface)),
			attr("user", s.PPPoE.User),
			attr("password", s.PPPoE.Password),
			fmt.Sprintf("add-default-route=%s", yesNo(s.PPPoE.DefaultRoute)),
			fmt.Sprintf("use-peer-dns=%s", yesNo(s.PPPoE.UsePeerDNS)),
			"dial-on-demand=no",
			"disabled=no",
		}
		if sn := strings.TrimSpace(s.PPPoE.ServiceName); sn != "" {
			attrs = append(attrs, attr("service-name", sn))
		}
		fmt.Fprintf(&b, "/interface pppoe-client add %s\n", strings.Join(attrs, " "))
		if s.PPPoE.NAT {
			fmt.Fprintf(&b, "/ip firewall nat remove [find comment=mti-pppoe]\n")
			fmt.Fprintf(&b, "/ip firewall nat add chain=srcnat %s action=masquerade comment=mti-pppoe\n",
				attr("out-interface", name))
		}
	}

	if s.WiFi.Enabled && avail[featWiFi] {
		anyOn = true
		section(&b, "Interface", "Wireless — Wi-Fi ("+wifiMode+")")
		ifs := strings.TrimSpace(s.WiFi.Interface)
		if wifiMode == "wave2" {
			fmt.Fprintf(&b, "/interface wifi configuration remove [find name=mti-wifi]\n")
			fmt.Fprintf(&b, "/interface wifi configuration add %s mode=ap %s %s %s\n",
				attr("name", "mti-wifi"),
				attr("ssid", strings.TrimSpace(s.WiFi.SSID)),
				attr("passphrase", s.WiFi.Passphrase),
				attr("country", strings.TrimSpace(s.WiFi.Country)))
			fmt.Fprintf(&b, "/interface wifi set %s configuration=mti-wifi disabled=no\n", rosQuote(ifs))
		} else {
			fmt.Fprintf(&b, "/interface wireless set %s disabled=no mode=ap %s country=%s authentication-types=wpa2-psk %s\n",
				rosQuote(ifs),
				attr("ssid", strings.TrimSpace(s.WiFi.SSID)),
				rosQuote(strings.TrimSpace(s.WiFi.Country)),
				attr("passphrase", s.WiFi.Passphrase))
		}
	}

	if !anyOn {
		b.WriteString("\n# Настройки не выбраны — отметьте разделы в меню конструктора.\n")
	}
	return b.String()
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
