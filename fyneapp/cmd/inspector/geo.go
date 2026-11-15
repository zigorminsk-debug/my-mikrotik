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
	ack                                            *widget.Check
	output                                         *widget.Entry
	status                                         *widget.Label
	refresh, load, check, preview                  *widget.Button
	interfaces                                     []mikrotik.Iface
	loadedCodes, loadedPrefixes                    []string
	revision                                       uint64
	busy                                           bool
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
	g.output.SetText("")
	g.status.SetText("Параметры изменены: проверку и предпросмотр нужно повторить.")
}
func (g *geoPanel) reset() {
	g.clearResult()
	g.interfaces = nil
	g.loadedCodes = nil
	g.loadedPrefixes = nil
	g.wan.Options = nil
	g.wan.ClearSelected()
	g.wan.Refresh()
	g.status.SetText("Подключитесь и заново прочитайте интерфейсы. Настройки не изменяются.")
}
func (g *geoPanel) setBusy(b bool) {
	g.busy = b
	fields := []fyne.Disableable{g.resource, g.destination, g.port, g.manual, g.countries, g.wan, g.protocol, g.ack}
	for _, v := range fields {
		if b {
			v.Disable()
		} else {
			v.Enable()
		}
	}
	_, device, ok := g.get()
	available := !b && ok && device != nil && (device.Protocol == "api" || device.Protocol == "rest")
	for _, btn := range []*widget.Button{g.refresh, g.load, g.check, g.preview} {
		if available {
			btn.Enable()
		} else {
			btn.Disable()
		}
	}
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
		fyne.Do(func() {
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
func newGeoPanel(get func() (mikrotik.Params, *mikrotik.Info, bool), start func(time.Duration) (context.Context, func() bool)) *geoPanel {
	g := &geoPanel{get: get, start: start}
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
	g.output = widget.NewMultiLineEntry()
	g.output.Wrapping = fyne.TextWrapOff
	g.output.SetMinRowsVisible(14)
	g.output.Disable()
	g.status = wrapLabel("Подключитесь и прочитайте интерфейсы. Применение в Fyne пока недоступно.")
	changed := func(string) { g.clearResult() }
	for _, e := range []*widget.Entry{g.resource, g.destination, g.port, g.manual} {
		e.OnChanged = changed
	}
	g.wan.OnChanged = changed
	g.protocol.OnChanged = changed
	g.ack.OnChanged = func(bool) { g.clearResult() }
	g.countries.OnChanged = func(string) { g.loadedCodes = nil; g.loadedPrefixes = nil; g.clearResult() }
	g.refresh = widget.NewButton("Прочитать интерфейсы", func() {
		p, _, ok := g.get()
		if !ok {
			return
		}
		g.interfaces = nil
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
			text, err := viewmodel.GeoPreview(device, ifaces, geo, time.Now().UTC())
			return func() {
				if err != nil {
					g.status.SetText(err.Error())
					return
				}
				g.output.SetText(text)
				g.status.SetText("Предпросмотр создан локально. Маршрутизация, доступность и успешность проверки dst-nat не подтверждены. Применение в Fyne пока недоступно.")
			}
		})
	})
	form := widget.NewForm(widget.NewFormItem("Идентификатор", g.resource), widget.NewFormItem("Внешний WAN", g.wan), widget.NewFormItem("IPv4 ресурса", g.destination), widget.NewFormItem("Внутренний порт", g.port), widget.NewFormItem("Протокол", g.protocol), widget.NewFormItem("Коды стран", g.countries), widget.NewFormItem("Дополнительные IPv4 / CIDR", g.manual))
	controls := container.NewVBox(wrapLabel("GeoNAT — проверка и предпросмотр. Настройки роутера не изменяются; перенос применения ещё не завершён. Ресурсом должен быть отдельный хост за роутером, не сервис самого роутера."), g.refresh, form, wrapLabel("Подсети стран загружаются с GitHub (ipverse/RIR); сервис увидит ваш публичный IP. Коды стран не гарантируют фактическое местоположение. Можно указать только свои IPv4/CIDR."), g.load, g.ack, g.check, g.preview, g.status)
	g.root = container.NewHSplit(container.NewVScroll(controls), g.output)
	g.setBusy(false)
	return g
}
