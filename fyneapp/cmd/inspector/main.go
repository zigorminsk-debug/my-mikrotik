package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/zigorminsk-debug/my-mikrotik/fyneapp/internal/viewmodel"
	"github.com/zigorminsk-debug/my-mikrotik/internal/server"
	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
)

var version = "0.5.0-fyne"

func main() {
	a := app.NewWithID("org.mikrotik.inspector.fyne")
	a.Settings().SetTheme(inspectorTheme{theme.LightTheme()})
	w := a.NewWindow("MikroTik Inspector — Fyne " + version)
	w.Resize(fyne.NewSize(960, 720))
	host := widget.NewEntry()
	host.SetPlaceHolder("192.168.88.1 (без http://)")
	host.SetText("192.168.88.1")
	port := widget.NewEntry()
	port.SetPlaceHolder("по умолчанию")
	user := widget.NewEntry()
	user.SetText("admin")
	password := widget.NewPasswordEntry()
	protocol := widget.NewSelect([]string{"auto", "api", "rest", "mac"}, nil)
	protocol.SetSelected("auto")
	tls := widget.NewCheck("TLS (сертификаты НЕ проверяются)", nil)
	topo := widget.NewCheck("Bridge, порты и VLAN", nil)
	leases := widget.NewCheck("Аренды DHCP: IP, MAC и имена", nil)
	history := widget.NewCheck("Краткая история mti-failover", nil)
	lte := widget.NewCheck("Однократное чтение LTE (RouterOS 7)", nil)
	out := widget.NewMultiLineEntry()
	out.Wrapping = fyne.TextWrapOff
	out.Disable()
	out.SetText("Подключитесь к роутеру. Вкладка GeoNAT поддерживает проверку, предпросмотр и применение с подтверждением.")
	status := widget.NewLabel("Диагностика не меняет настройки; GeoNAT можно применить после подтверждения. Экспериментальная версия без HTML и Edge.")
	status.Wrapping = fyne.TextWrapWord
	var info *mikrotik.Info
	var connected mikrotik.Params
	var cancel context.CancelFunc
	var seq uint64
	var busy bool
	var connect, connectDefault, snapshot, copyButton *widget.Button
	var geo *geoPanel
	var builder *builderPanel
	setBusy := func(b bool) {
		busy = b
		for _, e := range []*widget.Entry{host, port, user, password} {
			if b {
				e.Disable()
			} else {
				e.Enable()
			}
		}
		if b {
			protocol.Disable()
			tls.Disable()
			topo.Disable()
			leases.Disable()
			history.Disable()
			lte.Disable()
			connect.Disable()
			if connectDefault != nil {
				connectDefault.Disable()
			}
			snapshot.Disable()
		} else {
			protocol.Enable()
			tls.Enable()
			topo.Enable()
			leases.Enable()
			history.Enable()
			lte.Enable()
			connect.Enable()
			if connectDefault != nil {
				connectDefault.Enable()
			}
			if info != nil && info.Protocol != "mac" {
				snapshot.Enable()
			} else {
				snapshot.Disable()
			}
		}
		if geo != nil {
			geo.setBusy(b)
		}
		if builder != nil {
			builder.setBusy(b)
		}
	}
	invalidate := func() {
		seq++
		info = nil
		connected = mikrotik.Params{}
		if geo != nil {
			geo.reset()
			geo.setBusy(false)
		}
		if builder != nil {
			builder.reset()
		}
		if cancel != nil {
			cancel()
			cancel = nil
		}
		if snapshot != nil {
			snapshot.Disable()
		}
		if copyButton != nil {
			copyButton.Disable()
		}
		out.SetText("")
		status.SetText("Параметры изменены. Подключитесь заново.")
	}
	host.OnChanged = func(string) {
		if !busy {
			invalidate()
		}
	}
	port.OnChanged = host.OnChanged
	user.OnChanged = host.OnChanged
	password.OnChanged = host.OnChanged
	protocol.OnChanged = func(string) {
		if !busy {
			invalidate()
		}
	}
	tls.OnChanged = func(bool) {
		if !busy {
			invalidate()
		}
	}
	connect = widget.NewButton("Подключиться", func() {
		if busy {
			return
		}
		p := mikrotik.Params{Host: strings.TrimSpace(host.Text), User: strings.TrimSpace(user.Text), Password: password.Text, Protocol: protocol.Selected, TLS: tls.Checked}
		if strings.Contains(p.Host, "://") {
			dialog.ShowError(fmt.Errorf("укажите адрес без http:// или https:// и отдельно выберите протокол и TLS"), w)
			return
		}
		if port.Text != "" {
			n, err := strconv.Atoi(strings.TrimSpace(port.Text))
			if err != nil || n < 1 || n > 65535 {
				dialog.ShowError(fmt.Errorf("порт должен быть числом от 1 до 65535"), w)
				return
			}
			p.Port = n
		}
		seq++
		id := seq
		info = nil
		connected = mikrotik.Params{}
		if geo != nil {
			geo.reset()
		}
		if builder != nil {
			builder.reset()
		}
		copyButton.Disable()
		out.SetText("")
		setBusy(true)
		status.SetText("Подключение и чтение информации…")
		ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		cancel = stop
		go func() {
			result, err := mikrotik.Collect(ctx, p)
			stop()
			fyne.Do(func() {
				if id != seq {
					return
				}
				cancel = nil
				setBusy(false)
				if err != nil {
					status.SetText("Ошибка подключения: " + err.Error())
					return
				}
				info = result
				p.Protocol = result.Protocol
				connected = p
				if geo != nil {
					geo.setBusy(false)
				}
				out.SetText(viewmodel.Info(result))
				copyButton.Enable()
				if result.Protocol != "mac" {
					snapshot.Enable()
				}
				status.SetText("Подключено. Информация прочитана; настройки не изменены.")
				if builder != nil {
					builder.loadInterfaces()
				}
			})
		}()
	})
	connectDefault = widget.NewButton("Подключиться к новому MikroTik", func() {
		if busy || (geo != nil && (geo.confirming || geo.writing)) {
			return
		}
		setFactoryConnection(host, port, user, password, protocol, tls)
		connect.OnTapped()
	})
	snapshot = widget.NewButton("Собрать снимок", func() {
		if busy || info == nil {
			return
		}
		seq++
		id := seq
		p := connected
		device := info
		opts := mikrotik.NetworkSnapshotOptions{IncludeTopology: topo.Checked, IncludeLeases: leases.Checked, IncludeHistory: history.Checked, IncludeLTE: lte.Checked}
		copyButton.Disable()
		out.SetText("")
		setBusy(true)
		status.SetText("Чтение снимка…")
		ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
		cancel = stop
		go func() {
			result, err := mikrotik.CollectNetworkSnapshotWithOptions(ctx, p, device, opts)
			stop()
			fyne.Do(func() {
				if id != seq {
					return
				}
				cancel = nil
				setBusy(false)
				if err != nil {
					status.SetText("Ошибка снимка: " + err.Error())
					return
				}
				out.SetText(viewmodel.Snapshot(result))
				copyButton.Enable()
				status.SetText("Снимок готов. Проверьте частные сведения перед передачей.")
			})
		}()
	})
	snapshot.Disable()
	copyButton = widget.NewButton("Скопировать отчёт", func() {
		if out.Text != "" {
			w.Clipboard().SetContent(out.Text)
			status.SetText("Отчёт скопирован. Он может содержать частные сведения.")
		}
	})
	copyButton.Disable()
	clearReport := func(bool) {
		if !busy && info != nil {
			out.SetText("")
			copyButton.Disable()
			status.SetText("Параметры снимка изменены. Соберите снимок заново.")
		}
	}
	topo.OnChanged = clearReport
	leases.OnChanged = clearReport
	history.OnChanged = clearReport
	lte.OnChanged = clearReport
	connect.Importance = widget.HighImportance
	snapshot.Importance = widget.HighImportance
	fields := container.NewGridWithColumns(5, field("Адрес роутера или MAC", host), field("Порт (по умолчанию)", port), field("Логин", user), field("Пароль", password), field("Протокол", protocol))
	connectionCard := section("ПОДКЛЮЧЕНИЕ К РОУТЕРУ", fields, container.NewHBox(tls, connect, connectDefault), wrapLabel("Без TLS пароль и данные могут передаваться открытым текстом. TLS в этой версии не проверяет сертификат роутера."), status)
	out.SetMinRowsVisible(14)
	reportCard := section("ОТЧЁТ И СНИМОК СЕТИ", wrapLabel("Только чтение. Снимок может содержать частные адреса, имена и маршруты."), container.NewGridWithColumns(2, topo, leases, history, lte), container.NewHBox(snapshot, copyButton), reportViewer(out))
	getConnection := func() (mikrotik.Params, *mikrotik.Info, bool) { return connected, info, info != nil && !busy }
	startGeo := func(timeout time.Duration) (context.Context, func() bool) {
		seq++
		id := seq
		setBusy(true)
		ctx, stop := context.WithTimeout(context.Background(), timeout)
		cancel = stop
		return ctx, func() bool {
			stop()
			if id != seq {
				return false
			}
			cancel = nil
			setBusy(false)
			return true
		}
	}
	geo = newGeoPanel(getConnection, startGeo, func(title, message string, cb func(bool)) { dialog.ShowConfirm(title, message, cb, w) })
	var store deviceStore
	if dir, err := os.UserConfigDir(); err == nil {
		store = server.NewDeviceStore(filepath.Join(dir, "MikroTik Inspector", "devices.json"))
	}
	previousProtocol := "auto"
	var tabs *container.AppTabs
	devices := newDevicesPanel(store, func(address string) bool {
		if busy || geo.confirming || geo.writing {
			return false
		}
		fillSavedDevice(address, host, password, protocol, &previousProtocol, invalidate)
		tabs.SelectIndex(0)
		return true
	}, func(title, message string, cb func(bool)) { dialog.ShowConfirm(title, message, cb, w) },
		func(cb func(fyne.URIReadCloser, error)) { dialog.ShowFileOpen(cb, w) },
		func(cb func(fyne.URIWriteCloser, error)) {
			f := dialog.NewFileSave(cb, w)
			f.SetFileName("mikrotik-devices.json")
			f.Show()
		})
	information := container.NewVScroll(centeredPage(container.NewPadded(container.NewVBox(connectionCard, devices.root, reportCard))))
	builder = newBuilderPanel(getConnection, startGeo, func(text string) { w.Clipboard().SetContent(text) }, func(cb func(fyne.URIWriteCloser, error)) {
		f := dialog.NewFileSave(cb, w)
		f.SetFileName("mikrotik-config.rsc")
		f.Show()
	})
	constructor := container.NewAppTabs(container.NewTabItem("Меню RouterOS", builder.root), container.NewTabItem("GeoNAT", geo.root))
	tabs = container.NewAppTabs(container.NewTabItem("Информация", information), container.NewTabItem("Конструктор", constructor))
	w.SetContent(container.NewBorder(appHeader(version), nil, nil, nil, tabs))
	w.SetCloseIntercept(func() {
		if geo != nil && (geo.writing || geo.confirming) {
			dialog.ShowInformation("GeoNAT", "Завершите подтверждение или дождитесь результата применения; отключение питания или аварийное завершение могут оставить частичные изменения.", w)
			return
		}
		seq++
		if cancel != nil {
			cancel()
		}
		w.Close()
	})
	w.ShowAndRun()
}

// Selection is not a connection. Always discard old state, even for the same host.
func fillSavedDevice(address string, host, password *widget.Entry, protocol *widget.Select, previous *string, invalidate func()) {
	host.SetText(address)
	password.SetText("")
	mac, err := net.ParseMAC(address)
	if err == nil && len(mac) == 6 {
		if protocol.Selected != "mac" {
			*previous = protocol.Selected
		}
		protocol.SetSelected("mac")
	} else if protocol.Selected == "mac" {
		protocol.SetSelected(*previous)
	}
	invalidate()
}

// Matches the explicit factory-connection action from the HTML interface.
func setFactoryConnection(host, port, user, password *widget.Entry, protocol *widget.Select, tls *widget.Check) {
	host.SetText("192.168.88.1")
	port.SetText("")
	user.SetText("admin")
	password.SetText("")
	protocol.SetSelected("auto")
	tls.SetChecked(false)
}
