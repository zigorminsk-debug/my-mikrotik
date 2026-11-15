package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/zigorminsk-debug/my-mikrotik/fyneapp/internal/viewmodel"
	"github.com/zigorminsk-debug/my-mikrotik/internal/server"
	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
)

type geoPanel struct {
	root                                           fyne.CanvasObject
	resource, destination, port, manual, countries *widget.Entry
	wan, protocol                                  *widget.Select
	ack, unknown                                   *widget.Check
	output                                         *widget.Entry
	status                                         *widget.Label
	refresh, load, check, preview, apply           *widget.Button
	interfaces                                     []mikrotik.Iface
	loadedCodes, loadedPrefixes                    []string
	revision                                       uint64
	busy, writing, confirming                      bool
	plan                                           *viewmodel.GeoPlan
	planDevice                                     *mikrotik.Info
	confirm                                        func(string, string, func(bool))
	applyFn                                        func(context.Context, mikrotik.GeoApplyRequest) (string, error)
	dispatch                                       func(func())
	get                                            func() (mikrotik.Params, *mikrotik.Info, bool)
	start                                          func(time.Duration) (context.Context, func() bool)
}

func wrapLabel(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return l
}
func (g *geoPanel) clearResult() {
	g.revision++
	g.plan = nil
	g.planDevice = nil
	if g.apply != nil {
		g.apply.Disable()
	}
	g.output.SetText("")
	g.status.SetText("Параметры изменены: проверку и предпросмотр нужно повторить.")
}
func (g *geoPanel) reset() {
	g.clearResult()
	g.interfaces = nil
	g.loadedCodes = nil
	g.loadedPrefixes = nil
	// Acknowledgement for one router is not transferable to a new session.
	g.ack.SetChecked(false)
	g.unknown.SetChecked(false)
	g.wan.Options = nil
	g.wan.ClearSelected()
	g.wan.Refresh()
	g.status.SetText("Подключитесь и заново прочитайте интерфейсы. Настройки не изменяются.")
}
func (g *geoPanel) setBusy(b bool) {
	g.busy = b
	fields := []fyne.Disableable{g.resource, g.destination, g.port, g.manual, g.countries, g.wan, g.protocol, g.ack, g.unknown}
	for _, v := range fields {
		if b || g.confirming {
			v.Disable()
		} else {
			v.Enable()
		}
	}
	_, device, ok := g.get()
	available := !b && !g.confirming && ok && device != nil && (device.Protocol == "api" || device.Protocol == "rest")
	for _, btn := range []*widget.Button{g.refresh, g.load, g.check, g.preview} {
		if available {
			btn.Enable()
		} else {
			btn.Disable()
		}
	}
	if g.apply != nil {
		if available && g.planReady() {
			g.apply.Enable()
		} else {
			g.apply.Disable()
		}
	}
}
func (g *geoPanel) planReady() bool {
	conn, device, ok := g.get()
	if !ok || device == nil || (conn.Protocol != "api" && conn.Protocol != "rest") || device.Protocol != conn.Protocol || g.plan == nil || g.planDevice != device || !g.ack.Checked || !g.plan.Request.Geo.Acknowledged || !strings.HasPrefix(device.RouterOS, "7.") || device.Identity == "" || device.Architecture == "" {
		return false
	}
	age := time.Since(g.plan.Request.GeneratedAt)
	return age >= 0 && age <= 10*time.Minute && len(g.plan.Request.Script) > 0 && len(g.plan.Request.Script) <= mikrotik.MaxApplyScriptSize
}
func (g *geoPanel) params() mikrotik.GeoNATSettings {
	return mikrotik.GeoNATSettings{Enabled: true, Resource: strings.TrimSpace(g.resource.Text), WAN: g.wan.Selected, Destination: strings.TrimSpace(g.destination.Text), Port: strings.TrimSpace(g.port.Text), Protocol: g.protocol.Selected, Manual: g.manual.Text, Acknowledged: g.ack.Checked}
}
func (g *geoPanel) run(timeout time.Duration, f func(context.Context) func()) {
	if g.busy {
		return
	}
	ctx, finish := g.start(timeout)
	rev := g.revision
	go func() {
		complete := f(ctx)
		g.dispatch(func() {
			contextErr := ctx.Err()
			if !finish() {
				return
			}
			if contextErr != nil {
				g.status.SetText("Операция прервана или истекло время ожидания. Настройки не изменены.")
				return
			}
			if rev != g.revision {
				g.status.SetText("Параметры изменились во время операции. Повторите проверку.")
				return
			}
			complete()
		})
	}()
}
func newGeoPanel(get func() (mikrotik.Params, *mikrotik.Info, bool), start func(time.Duration) (context.Context, func() bool), confirm func(string, string, func(bool))) *geoPanel {
	g := &geoPanel{get: get, start: start, confirm: confirm, applyFn: mikrotik.ApplyGeoNAT, dispatch: fyne.Do}
	g.resource = widget.NewEntry()
	g.resource.SetPlaceHolder("например by")
	g.destination = widget.NewEntry()
	g.destination.SetPlaceHolder("192.168.88.50")
	g.port = widget.NewEntry()
	g.port.SetPlaceHolder("3389")
	g.manual = widget.NewMultiLineEntry()
	g.manual.SetPlaceHolder("203.0.113.5, 198.51.100.0/24")
	g.manual.SetMinRowsVisible(3)
	g.countries = widget.NewEntry()
	g.countries.SetPlaceHolder("by, pl — необязательно")
	g.wan = widget.NewSelect(nil, nil)
	g.protocol = widget.NewSelect([]string{"tcp", "udp"}, nil)
	g.protocol.SetSelected("tcp")
	g.ack = widget.NewCheck("Я проверил ресурс и WAN и сохранил резервную копию", nil)
	g.unknown = widget.NewCheck("Статус dst-nat не получен: я проверил вручную и разрешаю продолжить без него", nil)
	g.output = widget.NewMultiLineEntry()
	g.output.Wrapping = fyne.TextWrapOff
	g.output.SetMinRowsVisible(14)
	g.output.Disable()
	g.status = wrapLabel("Подключитесь и прочитайте интерфейсы. Перед применением сохраните резервную копию.")
	invalidateAcknowledgement := func() {
		if g.ack.Checked {
			g.ack.SetChecked(false)
		} else {
			g.clearResult()
		}
	}
	changed := func(string) {
		// Unknown-status consent belongs to the old exact target, never the new one.
		if g.unknown.Checked {
			g.unknown.SetChecked(false)
		}
		invalidateAcknowledgement()
	}
	for _, e := range []*widget.Entry{g.resource, g.destination, g.port, g.manual} {
		e.OnChanged = changed
	}
	g.wan.OnChanged = changed
	g.protocol.OnChanged = changed
	g.ack.OnChanged = func(bool) { g.clearResult() }
	g.unknown.OnChanged = func(bool) { invalidateAcknowledgement() }
	g.countries.OnChanged = func(string) { g.loadedCodes = nil; g.loadedPrefixes = nil; changed("") }
	g.refresh = widget.NewButton("Прочитать интерфейсы", func() {
		p, _, ok := g.get()
		if !ok {
			return
		}
		g.interfaces = nil
		g.unknown.SetChecked(false)
		g.ack.SetChecked(false)
		g.clearResult()
		g.status.SetText("Чтение интерфейсов и портов bridge…")
		g.run(30*time.Second, func(ctx context.Context) func() {
			v, err := mikrotik.CollectInterfaces(ctx, p)
			return func() {
				if err != nil {
					g.status.SetText("Интерфейсы не прочитаны: " + err.Error())
					return
				}
				g.interfaces = v
				names := []string{}
				for _, i := range v {
					if !i.Disabled && i.Name != "" {
						names = append(names, i.Name)
					}
				}
				sort.Strings(names)
				g.wan.Options = names
				g.wan.ClearSelected()
				g.wan.Refresh()
				if len(names) == 0 {
					g.status.SetText("Включённые интерфейсы не найдены.")
				} else {
					g.status.SetText("Интерфейсы прочитаны. Выберите внешний WAN; совпадение данных не подтверждает физическую идентичность устройства.")
				}
			}
		})
	})
	g.load = widget.NewButton("Загрузить IPv4 стран", func() {
		codes, err := viewmodel.CountryCodes(g.countries.Text)
		if err != nil || len(codes) == 0 {
			g.status.SetText("Укажите от 1 до 12 разных двухбуквенных кодов стран.")
			return
		}
		g.loadedCodes = nil
		g.loadedPrefixes = nil
		g.unknown.SetChecked(false)
		g.ack.SetChecked(false)
		g.clearResult()
		g.status.SetText("Запрашиваем публичные данные у GitHub / ipverse…")
		g.run(90*time.Second, func(ctx context.Context) func() {
			prefixes, when, err := server.GeoCountryPrefixes(ctx, codes)
			return func() {
				if err != nil {
					g.status.SetText("Загрузка не выполнена: " + err.Error())
					return
				}
				g.loadedCodes = append([]string(nil), codes...)
				g.loadedPrefixes = append([]string(nil), prefixes...)
				g.status.SetText(fmt.Sprintf("Загружено %d подсетей, время получения %s. Распределение адресов не гарантирует местоположение.", len(prefixes), when.UTC().Format(time.RFC3339)))
			}
		})
	})
	g.check = widget.NewButton("Проверить dst-nat (только чтение)", func() {
		p, device, ok := g.get()
		if !ok {
			return
		}
		geo := g.params()
		g.clearResult()
		g.status.SetText("Повторно проверяем устройство и существующий dst-nat…")
		g.run(60*time.Second, func(ctx context.Context) func() {
			matched, message, err := mikrotik.DiagnoseGeoNAT(ctx, mikrotik.GeoApplyRequest{Conn: p, Device: device, Geo: geo})
			return func() {
				if err != nil {
					g.status.SetText("Проверка не выполнена: " + err.Error() + ". Настройки не изменены.")
					return
				}
				if !matched {
					g.status.SetText("Проверка не пройдена: " + message + ". Настройки не изменены.")
					return
				}
				g.status.SetText(message)
			}
		})
	})
	g.preview = widget.NewButton("Создать предпросмотр (без записи)", func() {
		_, device, ok := g.get()
		if !ok {
			return
		}
		if len(g.interfaces) == 0 {
			g.status.SetText("Сначала прочитайте интерфейсы.")
			return
		}
		codes, err := viewmodel.CountryCodes(g.countries.Text)
		if err != nil {
			g.status.SetText(err.Error())
			return
		}
		if len(codes) > 0 && strings.Join(codes, ",") != strings.Join(g.loadedCodes, ",") {
			g.status.SetText("Выбор стран изменился: загрузите подсети заново.")
			return
		}
		geo := g.params()
		geo.Countries = append([]string(nil), codes...)
		geo.CountryPrefixes = append([]string(nil), g.loadedPrefixes...)
		ifaces := append([]mikrotik.Iface(nil), g.interfaces...)
		g.clearResult()
		g.status.SetText("Формирование предпросмотра…")
		g.run(30*time.Second, func(ctx context.Context) func() {
			if ctx.Err() != nil {
				return func() { g.status.SetText("Предпросмотр отменён.") }
			}
			plan, err := viewmodel.BuildGeoPlan(device, ifaces, geo, time.Now().UTC())
			return func() {
				if err != nil {
					g.status.SetText(err.Error())
					return
				}
				g.plan = plan
				g.planDevice = device
				g.output.SetText(plan.Display)
				g.setBusy(false)
				if len(plan.Request.Script) > mikrotik.MaxApplyScriptSize {
					g.status.SetText("Предпросмотр создан, но скрипт превышает 64 КиБ: автоматическое применение недоступно.")
					return
				}
				if !g.planReady() {
					g.status.SetText("Предпросмотр создан, но для применения нужны RouterOS 7 и полные данные устройства API/REST. Настройки не изменены.")
					return
				}
				g.status.SetText("Предпросмотр создан. Применение повторно проверит устройство и dst-nat; доступность и защита не подтверждены.")
			}
		})
	})

	g.apply = widget.NewButton("Применить GeoNAT…", func() {
		if g.busy || g.confirming || !g.planReady() {
			g.status.SetText("Создайте свежий предпросмотр и подтвердите резервную копию.")
			return
		}
		conn, device, ok := g.get()
		if !ok {
			return
		}
		plan := g.plan
		rev := g.revision
		unknown := g.unknown.Checked
		warning := ""
		if unknown {
			warning = "ВНИМАНИЕ: если статус disabled отсутствует, применение продолжится без подтверждения включённого состояния dst-nat. Вы проверили это вручную?\n\n"
		}
		message := fmt.Sprintf("Устройство: %s (%s), WAN: %s, идентификатор: %s, ресурс: %s:%s/%s. Страны: %s; подсетей стран: %d; ручные адреса заданы: %t.\n\n%sБудут добавлены адреса и правила forward; accept может обойти прежние ограничения firewall. Другие пробросы на этот адрес/порт также затрагиваются. Старый набор с тем же идентификатором заменяется; служебный скрипт mti-apply перезаписывается. IPv6 и сервисы роутера не защищены; старые FastTrack-соединения могут сохраняться.\n\nСбой может оставить частичные изменения, отката нет. Резервная копия сохранена и локальный доступ есть?", conn.Host, device.Identity, plan.Request.Geo.WAN, plan.Request.Geo.Resource, plan.Request.Geo.Destination, plan.Request.Geo.Port, plan.Request.Geo.Protocol, strings.Join(plan.Request.Geo.Countries, ", "), len(plan.Request.Geo.CountryPrefixes), strings.TrimSpace(plan.Request.Geo.Manual) != "", warning)
		g.confirming = true
		g.setBusy(false)
		g.confirm("Подтвердите применение GeoNAT", message, func(accepted bool) {
			if !g.confirming {
				return
			}
			g.confirming = false
			g.setBusy(false)
			if !accepted {
				g.status.SetText("Применение отменено. Настройки не изменены.")
				return
			}
			current, currentDevice, currentOK := g.get()
			if g.busy || !currentOK || current != conn || currentDevice != device || g.revision != rev || g.plan != plan || g.unknown.Checked != unknown || !g.planReady() {
				g.status.SetText("Данные изменились после подтверждения. Применение не запускалось.")
				return
			}
			req := plan.Request
			req.Conn = current
			req.AllowUnknownNATStatus = unknown
			g.plan = nil
			g.planDevice = nil
			g.writing = true
			g.status.SetText("Проверяем устройство и применяем. Не закрывайте программу…")
			ctx, finish := g.start(120 * time.Second)
			go func() {
				_, err := g.applyFn(ctx, req)
				g.dispatch(func() {
					contextErr := ctx.Err()
					if !finish() {
						return
					}
					g.writing = false
					g.ack.SetChecked(false)
					g.unknown.SetChecked(false)
					g.clearResult()
					g.setBusy(false)
					g.status.SetText(viewmodel.GeoApplyOutcome(err, contextErr))
				})
			}()
		})
	})
	form := widget.NewForm(widget.NewFormItem("Идентификатор", g.resource), widget.NewFormItem("Внешний WAN", g.wan), widget.NewFormItem("IPv4 ресурса", g.destination), widget.NewFormItem("Внутренний порт", g.port), widget.NewFormItem("Протокол", g.protocol), widget.NewFormItem("Коды стран", g.countries), widget.NewFormItem("Дополнительные IPv4 / CIDR", g.manual))
	controls := container.NewVBox(wrapLabel("GeoNAT — проверка, предпросмотр и применение с подтверждением. Применение изменяет firewall, возможно частичное выполнение; отката нет. Ресурсом должен быть отдельный хост за роутером, не сервис самого роутера."), g.refresh, form, wrapLabel("Подсети стран загружаются с GitHub (ipverse/RIR); сервис увидит ваш публичный IP. Коды стран не гарантируют фактическое местоположение. Можно указать только свои IPv4/CIDR."), g.load, g.unknown, g.ack, g.check, g.preview, g.apply, g.status)
	g.root = container.NewHSplit(container.NewVScroll(controls), g.output)
	g.setBusy(false)
	return g
}
