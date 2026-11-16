package main

import (
	"context"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
)

// builderPanel currently supports the common IP/System/PPP/Wireless sections.
// This stage does not call any RouterOS write operation.
type builderPanel struct {
	root                                                                           fyne.CanvasObject
	get                                                                            func() (mikrotik.Params, *mikrotik.Info, bool)
	start                                                                          func(time.Duration) (context.Context, func() bool)
	read                                                                           func(context.Context, mikrotik.Params) ([]mikrotik.Iface, error)
	dispatch                                                                       func(func())
	copy                                                                           func(string)
	saveFile                                                                       func(func(fyne.URIWriteCloser, error))
	busy, suppress, fileOpen, loaded                                               bool
	revision                                                                       uint64
	conn                                                                           mikrotik.Params
	device                                                                         *mikrotik.Info
	ifaces                                                                         []mikrotik.Iface
	features                                                                       map[string]mikrotik.FeatureInfo
	checks                                                                         map[string]*widget.Check
	bodies                                                                         map[string]*fyne.Container
	notes                                                                          map[string]*widget.Label
	inputs                                                                         map[string][]fyne.Disableable
	lanIface, dhcpIface, pppoeIface, wifiIface, lease                              *widget.Select
	lanAddr, poolStart, poolEnd, dnsServers, ntpServers, ntpZone                   *widget.Entry
	pppoeName, pppoeUser, pppoePass, pppoeService, wifiSSID, wifiPass, wifiCountry *widget.Entry
	dnsRemote, pppoeRoute, pppoeDNS, pppoeNAT                                      *widget.Check
	refresh, copyButton, download                                                  *widget.Button
	output                                                                         *widget.Entry
	status                                                                         *widget.Label
	script                                                                         string
}

func newBuilderPanel(get func() (mikrotik.Params, *mikrotik.Info, bool), start func(time.Duration) (context.Context, func() bool), copyFn func(string), saveFn func(func(fyne.URIWriteCloser, error))) *builderPanel {
	b := &builderPanel{get: get, start: start, read: mikrotik.CollectInterfaces, dispatch: fyne.Do, copy: copyFn, saveFile: saveFn, checks: map[string]*widget.Check{}, bodies: map[string]*fyne.Container{}, notes: map[string]*widget.Label{}, inputs: map[string][]fyne.Disableable{}}
	entry := func(value string) *widget.Entry { e := widget.NewEntry(); e.SetText(value); return e }
	b.lanIface = widget.NewSelect(nil, nil)
	b.dhcpIface = widget.NewSelect(nil, nil)
	b.pppoeIface = widget.NewSelect(nil, nil)
	b.wifiIface = widget.NewSelect(nil, nil)
	b.lease = widget.NewSelect([]string{"10m", "30m", "1h", "3h", "12h", "1d", "3d", "7d"}, nil)
	b.lease.SetSelected("1d")
	b.lanAddr = entry("192.168.88.1/24")
	b.poolStart = entry("")
	b.poolEnd = entry("")
	b.poolStart.SetPlaceHolder("авто")
	b.poolEnd.SetPlaceHolder("авто")
	b.dnsServers = entry("8.8.8.8, 8.8.4.4")
	b.ntpServers = entry("pool.ntp.org")
	b.ntpZone = entry("Europe/Minsk")
	b.pppoeName = entry("pppoe-out1")
	b.pppoeUser = entry("")
	b.pppoePass = widget.NewPasswordEntry()
	b.pppoeService = entry("")
	b.wifiSSID = entry("")
	b.wifiPass = widget.NewPasswordEntry()
	b.wifiCountry = entry("BY")
	b.dnsRemote = widget.NewCheck("Разрешить запросы клиентов", nil)
	b.dnsRemote.SetChecked(true)
	b.pppoeRoute = widget.NewCheck("Маршрут по умолчанию", nil)
	b.pppoeRoute.SetChecked(true)
	b.pppoeDNS = widget.NewCheck("DNS провайдера", nil)
	b.pppoeDNS.SetChecked(true)
	b.pppoeNAT = widget.NewCheck("NAT masquerade", nil)
	b.pppoeNAT.SetChecked(true)
	b.status = wrapLabel("Подключитесь и прочитайте интерфейсы. Изменения не выполняются.")
	b.output = widget.NewMultiLineEntry()
	b.output.Wrapping = fyne.TextWrapOff
	b.output.SetMinRowsVisible(18)
	b.output.Disable()
	b.refresh = widget.NewButton("Обновить данные устройства", b.loadInterfaces)
	b.copyButton = widget.NewButton("Скопировать для New Terminal", func() {
		if b.current() && b.script != "" {
			b.copy(b.script)
			b.status.SetText("Скрипт скопирован. В нём могут быть пароли; выполнение изменяет настройки.")
		}
	})
	b.download = widget.NewButton("Скачать .rsc…", b.downloadScript)
	b.copyButton.Importance = widget.HighImportance
	makeSection := func(id, title string, inputs []fyne.Disableable, content ...fyne.CanvasObject) fyne.CanvasObject {
		check := widget.NewCheck(title, nil)
		note := wrapLabel("")
		body := container.NewVBox(content...)
		b.checks[id] = check
		b.notes[id] = note
		b.bodies[id] = body
		b.inputs[id] = inputs
		check.OnChanged = func(bool) { b.changed() }
		return container.NewVBox(check, note, body, widget.NewSeparator())
	}
	lan := makeSection("lan", "Addresses — адрес и подсеть", []fyne.Disableable{b.lanIface, b.lanAddr}, field("Интерфейс LAN", b.lanIface), field("Адрес роутера (IPv4/CIDR)", b.lanAddr), wrapLabel("Смена LAN может оборвать доступ. Общий скрипт содержит только небезопасный предпросмотр; отдельный LAN-план пока не перенесён."))
	dhcp := makeSection("dhcp", "DHCP Server — раздача адресов", []fyne.Disableable{b.dhcpIface, b.lease, b.poolStart, b.poolEnd}, field("Интерфейс DHCP", b.dhcpIface), field("Время аренды", b.lease), field("Пул: с", b.poolStart), field("Пул: по", b.poolEnd), wrapLabel("Оставьте оба поля пустыми для автоматического расчёта пула."))
	dns := makeSection("dns", "DNS — серверы имён", []fyne.Disableable{b.dnsServers, b.dnsRemote}, field("DNS-серверы через запятую", b.dnsServers), b.dnsRemote)
	ntp := makeSection("ntp", "NTP Client — точное время", []fyne.Disableable{b.ntpServers, b.ntpZone}, field("Серверы времени", b.ntpServers), field("Часовой пояс", b.ntpZone))
	pppoe := makeSection("pppoe", "PPPoE Client — подключение к интернету", []fyne.Disableable{b.pppoeIface, b.pppoeName, b.pppoeUser, b.pppoePass, b.pppoeService, b.pppoeRoute, b.pppoeDNS, b.pppoeNAT}, field("WAN-интерфейс", b.pppoeIface), field("Имя подключения", b.pppoeName), field("Логин провайдера", b.pppoeUser), field("Пароль провайдера", b.pppoePass), field("Имя сервиса (необязательно)", b.pppoeService), b.pppoeRoute, b.pppoeDNS, b.pppoeNAT)
	wifi := makeSection("wifi", "Wireless — Wi-Fi сеть", []fyne.Disableable{b.wifiIface, b.wifiSSID, b.wifiPass, b.wifiCountry}, field("Беспроводной интерфейс", b.wifiIface), field("Имя сети (SSID)", b.wifiSSID), field("Пароль (8–63 символа)", b.wifiPass), field("Страна RouterOS или код", b.wifiCountry))
	for _, e := range []*widget.Entry{b.lanAddr, b.poolStart, b.poolEnd, b.dnsServers, b.ntpServers, b.ntpZone, b.pppoeName, b.pppoeUser, b.pppoePass, b.pppoeService, b.wifiSSID, b.wifiPass, b.wifiCountry} {
		e.OnChanged = func(string) { b.changed() }
	}
	for _, s := range []*widget.Select{b.lanIface, b.dhcpIface, b.pppoeIface, b.wifiIface, b.lease} {
		s.OnChanged = func(string) { b.changed() }
	}
	for _, c := range []*widget.Check{b.dnsRemote, b.pppoeRoute, b.pppoeDNS, b.pppoeNAT} {
		c.OnChanged = func(bool) { b.changed() }
	}
	left := section("МЕНЮ ROUTEROS", b.refresh, lan, dhcp, dns, ntp, pppoe, wifi, wrapLabel("WAN, VPN, RADIUS, резервное копирование и другие разделы ещё не перенесены."))
	right := section("СКРИПТ ДЛЯ NEW TERMINAL", wrapLabel("Предпросмотр генерируется локально. Скрипт исполняемый и может содержать пароли. Перед выполнением сохраните резервную копию и проверьте доступ; автоматического отката нет."), reportViewer(b.output), container.NewHBox(b.copyButton, b.download), wrapLabel("Применение этого общего скрипта из Fyne пока не реализовано. GeoNAT доступен отдельно."), b.status)
	b.root = container.NewHSplit(container.NewVScroll(left), container.NewVScroll(right))
	b.controls()
	return b
}
func (b *builderPanel) current() bool {
	p, d, ok := b.get()
	return ok && !b.busy && !b.fileOpen && b.loaded && d != nil && d == b.device && p == b.conn
}
func (b *builderPanel) controls() {
	current := b.current()
	_, d, ok := b.get()
	if !b.busy && !b.fileOpen && ok && d != nil {
		b.refresh.Enable()
	} else {
		b.refresh.Disable()
	}
	for id, c := range b.checks {
		f, exists := b.features[id]
		available := current && exists && f.Available
		if available {
			c.Enable()
			b.notes[id].SetText("")
		} else {
			c.Disable()
			if current && exists && !f.Available {
				b.notes[id].SetText(f.Reason)
			} else {
				b.notes[id].SetText("")
			}
		}
		if c.Checked {
			b.bodies[id].Show()
		} else {
			b.bodies[id].Hide()
		}
		for _, v := range b.inputs[id] {
			if available && c.Checked {
				v.Enable()
			} else {
				v.Disable()
			}
		}
	}
	if current && b.script != "" {
		b.copyButton.Enable()
		b.download.Enable()
	} else {
		b.copyButton.Disable()
		b.download.Disable()
	}
}
func (b *builderPanel) setBusy(busy bool) { b.busy = busy; b.controls() }
func (b *builderPanel) settings() mikrotik.Settings {
	return mikrotik.Settings{
		LAN:   mikrotik.LanSettings{Enabled: b.checks["lan"].Checked, Interface: b.lanIface.Selected, Address: strings.TrimSpace(b.lanAddr.Text)},
		DHCP:  mikrotik.DHCPSettings{Enabled: b.checks["dhcp"].Checked, Interface: b.dhcpIface.Selected, PoolStart: strings.TrimSpace(b.poolStart.Text), PoolEnd: strings.TrimSpace(b.poolEnd.Text), LeaseTime: b.lease.Selected},
		DNS:   mikrotik.DNSSettings{Enabled: b.checks["dns"].Checked, Servers: strings.TrimSpace(b.dnsServers.Text), AllowRemote: b.dnsRemote.Checked},
		NTP:   mikrotik.NTPSettings{Enabled: b.checks["ntp"].Checked, Servers: strings.TrimSpace(b.ntpServers.Text), TimeZone: strings.TrimSpace(b.ntpZone.Text)},
		PPPoE: mikrotik.PPPoESettings{Enabled: b.checks["pppoe"].Checked, Interface: b.pppoeIface.Selected, Name: strings.TrimSpace(b.pppoeName.Text), User: strings.TrimSpace(b.pppoeUser.Text), Password: b.pppoePass.Text, ServiceName: strings.TrimSpace(b.pppoeService.Text), DefaultRoute: b.pppoeRoute.Checked, UsePeerDNS: b.pppoeDNS.Checked, NAT: b.pppoeNAT.Checked},
		WiFi:  mikrotik.WiFiSettings{Enabled: b.checks["wifi"].Checked, Interface: b.wifiIface.Selected, SSID: strings.TrimSpace(b.wifiSSID.Text), Passphrase: b.wifiPass.Text, Country: strings.TrimSpace(b.wifiCountry.Text)},
	}
}
func (b *builderPanel) changed() {
	if b.suppress {
		return
	}
	b.revision++
	b.script = ""
	b.output.SetText("")
	if !b.current() {
		b.controls()
		return
	}
	s := b.settings()
	r := mikrotik.Generate(b.device, b.ifaces, &s, time.Now())
	if !r.OK {
		b.status.SetText("Проверьте настройки: " + r.Error)
		b.controls()
		return
	}
	if len(r.Skipped) > 0 {
		b.status.SetText("Включённый раздел недоступен; проверьте пакеты и интерфейсы.")
		b.controls()
		return
	}
	b.output.SetText(r.Script)
	if s.LAN.Enabled || s.DHCP.Enabled || s.DNS.Enabled || s.NTP.Enabled || s.PPPoE.Enabled || s.WiFi.Enabled {
		b.script = r.Script
		b.status.SetText("Предпросмотр готов. Это не проверка текущих настроек или доступности устройства.")
	} else {
		b.status.SetText("Нет включённых разделов. Настройки не изменены.")
	}
	b.controls()
}
func (b *builderPanel) reset() {
	b.suppress = true
	b.revision++
	b.script = ""
	b.output.SetText("")
	b.loaded = false
	b.device = nil
	b.conn = mikrotik.Params{}
	b.ifaces = nil
	b.features = nil
	for _, c := range b.checks {
		c.SetChecked(false)
	}
	for _, s := range []*widget.Select{b.lanIface, b.dhcpIface, b.pppoeIface, b.wifiIface} {
		s.ClearSelected()
		s.Options = nil
		s.Refresh()
	}
	b.pppoePass.SetText("")
	b.wifiPass.SetText("")
	b.suppress = false
	b.controls()
	b.status.SetText("Подключитесь и прочитайте интерфейсы. Предыдущие планы и пароли конструктора очищены.")
}
func (b *builderPanel) loadInterfaces() {
	if b.busy || b.fileOpen {
		return
	}
	p, d, ok := b.get()
	if !ok || d == nil {
		return
	}
	b.revision++
	rev := b.revision
	b.loaded = false
	b.script = ""
	b.output.SetText("")
	b.controls()
	b.status.SetText("Чтение интерфейсов и портов bridge…")
	ctx, finish := b.start(30 * time.Second)
	go func() {
		list, err := b.read(ctx, p)
		ctxErr := ctx.Err()
		b.dispatch(func() {
			if !finish() {
				return
			}
			now, device, ok := b.get()
			if b.revision != rev || !ok || now != p || device != d {
				return
			}
			if err != nil || ctxErr != nil {
				b.loaded = false
				b.controls()
				b.status.SetText("Интерфейсы не прочитаны; обновите данные устройства.")
				return
			}
			b.setInterfaces(p, d, list)
		})
	}()
}
func (b *builderPanel) setInterfaces(p mikrotik.Params, d *mikrotik.Info, list []mikrotik.Iface) {
	b.suppress = true
	b.conn = p
	b.device = d
	b.ifaces = append([]mikrotik.Iface(nil), list...)
	b.features = map[string]mikrotik.FeatureInfo{}
	for _, f := range mikrotik.AnalyzeFeatures(d, b.ifaces) {
		b.features[f.ID] = f
	}
	var names, wifi []string
	preferredLAN, preferredWAN := "", ""
	for _, i := range list {
		if i.Name == "" || i.Disabled {
			continue
		}
		names = append(names, i.Name)
		if mikrotik.IsWiFiIface(i) {
			wifi = append(wifi, i.Name)
		}
		if i.Type == "bridge" && preferredLAN == "" {
			preferredLAN = i.Name
		}
		if i.Type == "ether" || i.Type == "ethernet" {
			if preferredWAN == "" {
				preferredWAN = i.Name
			}
		}
	}
	sort.Strings(names)
	sort.Strings(wifi)
	if preferredLAN == "" && len(names) > 0 {
		preferredLAN = names[0]
	}
	if preferredWAN == "" && len(names) > 0 {
		preferredWAN = names[0]
	}
	set := func(s *widget.Select, opts []string, value string) {
		prior := s.Selected
		for _, option := range opts {
			if prior != "" && option == prior {
				value = prior
				break
			}
		}
		s.Options = append([]string(nil), opts...)
		s.ClearSelected()
		if value != "" {
			s.SetSelected(value)
		}
		s.Refresh()
	}
	set(b.lanIface, names, preferredLAN)
	set(b.dhcpIface, names, preferredLAN)
	set(b.pppoeIface, names, preferredWAN)
	firstWiFi := ""
	if len(wifi) > 0 {
		firstWiFi = wifi[0]
	}
	set(b.wifiIface, wifi, firstWiFi)
	for id, c := range b.checks {
		if f, ok := b.features[id]; !ok || !f.Available {
			c.SetChecked(false)
		}
	}
	b.loaded = true
	b.suppress = false
	b.changed()
}
func (b *builderPanel) downloadScript() {
	if !b.current() || b.script == "" {
		return
	}
	rev, source := b.revision, b.script
	b.fileOpen = true
	b.controls()
	b.saveFile(func(w fyne.URIWriteCloser, err error) {
		if !b.fileOpen {
			if w != nil {
				w.Close()
			}
			return
		}
		b.fileOpen = false
		b.controls()
		if err != nil {
			if w != nil {
				w.Close()
			}
			b.status.SetText("Не удалось открыть файл; он мог быть изменён.")
			return
		}
		if w == nil {
			return
		}
		if b.revision != rev || b.script != source || !b.current() {
			w.Close()
			b.status.SetText("Данные изменились. Скрипт не сохранён; выбранный файл мог быть создан или изменён.")
			return
		}
		n, e := w.Write([]byte(source))
		closeErr := w.Close()
		if e != nil || closeErr != nil || n != len(source) {
			b.status.SetText("Ошибка сохранения; файл может быть неполным.")
			return
		}
		b.status.SetText("Файл сохранён. Он исполняемый и может содержать пароли; храните его защищённо.")
	})
}
