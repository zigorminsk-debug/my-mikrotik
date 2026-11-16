package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/zigorminsk-debug/my-mikrotik/internal/server"
)

type deviceStore interface {
	Load() ([]server.SavedDevice, string, error)
	Save(string, []server.SavedDevice) (string, error)
}
type devicesPanel struct {
	root                                                          fyne.CanvasObject
	store                                                         deviceStore
	entries                                                       []server.SavedDevice
	revision                                                      string
	selected                                                      int
	modal                                                         bool
	modalSeq                                                      uint64
	selectDevice                                                  *widget.Select
	name, host, note                                              *widget.Entry
	status                                                        *widget.Label
	add, update, remove, use, refresh, importButton, exportButton *widget.Button
	applyHost                                                     func(string) bool
	confirm                                                       func(string, string, func(bool))
	open                                                          func(func(fyne.URIReadCloser, error))
	saveFile                                                      func(func(fyne.URIWriteCloser, error))
}

func newDevicesPanel(store deviceStore, use func(string) bool, confirm func(string, string, func(bool)), open func(func(fyne.URIReadCloser, error)), saveFile func(func(fyne.URIWriteCloser, error))) *devicesPanel {
	d := &devicesPanel{store: store, selected: -1, applyHost: use, confirm: confirm, open: open, saveFile: saveFile}
	d.name = widget.NewEntry()
	d.host = widget.NewEntry()
	d.note = widget.NewEntry()
	d.status = wrapLabel("")
	d.selectDevice = widget.NewSelect(nil, func(name string) {
		d.selected = -1
		for i, e := range d.entries {
			if e.Name == name {
				d.selected = i
				d.name.SetText(e.Name)
				d.host.SetText(e.Host)
				d.note.SetText(e.Note)
				break
			}
		}
		d.controls()
	})
	d.add = widget.NewButton("Добавить", func() {
		next := append([]server.SavedDevice{}, d.entries...)
		next = append(next, d.editor())
		d.save(next, "Запись добавлена.")
	})
	d.update = widget.NewButton("Сохранить изменения", func() {
		if !d.isSelected() {
			return
		}
		next := append([]server.SavedDevice{}, d.entries...)
		next[d.selected] = d.editor()
		d.save(next, "Изменения сохранены.")
	})
	d.remove = widget.NewButton("Удалить…", func() {
		if !d.isSelected() || d.modal {
			return
		}
		idx, rev := d.selected, d.revision
		d.modal = true
		d.modalSeq++
		token := d.modalSeq
		d.controls()
		d.confirm("Удаление записи", "Удалить выбранную запись из локального списка?", func(ok bool) {
			if !d.modal || d.modalSeq != token {
				return
			}
			d.modal = false
			d.controls()
			if !ok {
				return
			}
			if d.revision != rev || d.selected != idx || !d.isSelected() {
				d.status.SetText("Список изменился. Обновите и повторите.")
				return
			}
			next := append([]server.SavedDevice{}, d.entries[:idx]...)
			next = append(next, d.entries[idx+1:]...)
			d.save(next, "Запись удалена.")
		})
	})
	d.use = widget.NewButton("Подставить адрес", func() {
		if !d.isSelected() || d.modal {
			return
		}
		if !d.applyHost(d.entries[d.selected].Host) {
			d.status.SetText("Дождитесь завершения подключения или применения.")
			return
		}
		d.status.SetText("Адрес подставлен, пароль очищен. Проверьте параметры и подключитесь вручную.")
	})
	d.refresh = widget.NewButton("Обновить список", d.reload)
	d.importButton = widget.NewButton("Добавить из JSON…", d.importFile)
	d.exportButton = widget.NewButton("Экспорт JSON…", d.exportFile)
	form := container.NewGridWithColumns(4, field("Сохранённая запись", d.selectDevice), field("Название", d.name), field("Адрес роутера или MAC", d.host), field("Заметка (без паролей)", d.note))
	d.add.Importance = widget.HighImportance
	d.root = section("СОХРАНЁННЫЕ УСТРОЙСТВА", wrapLabel("Локальный список без паролей. Не записывайте секреты: файл и экспорт не зашифрованы. Адрес может устареть и не подтверждает личность устройства."), form, container.NewHBox(d.add, d.update, d.remove, d.use, d.refresh), container.NewHBox(d.exportButton, d.importButton), wrapLabel("Подстановка очищает пароль и не подключается. Проверьте логин, порт, протокол и TLS. Импорт добавляет записи, не заменяя список; повторные названия отклоняются. При экспорте не выбирайте рабочий файл списка или важный файл: он может быть перезаписан."), d.status)
	d.reload()
	return d
}
func (d *devicesPanel) isSelected() bool {
	return d.revision != "" && d.selected >= 0 && d.selected < len(d.entries)
}
func (d *devicesPanel) controls() {
	ready := d.revision != "" && !d.modal
	for _, v := range []fyne.Disableable{d.name, d.host, d.note, d.selectDevice, d.add, d.importButton, d.exportButton} {
		if ready {
			v.Enable()
		} else {
			v.Disable()
		}
	}
	for _, v := range []*widget.Button{d.update, d.remove, d.use} {
		if ready && d.isSelected() {
			v.Enable()
		} else {
			v.Disable()
		}
	}
	if d.modal {
		d.refresh.Disable()
	} else {
		d.refresh.Enable()
	}
}
func (d *devicesPanel) render() {
	d.selected = -1
	d.selectDevice.ClearSelected()
	d.selectDevice.Options = nil
	for _, e := range d.entries {
		d.selectDevice.Options = append(d.selectDevice.Options, e.Name)
	}
	d.selectDevice.Refresh()
	d.controls()
}
func (d *devicesPanel) reload() {
	if d.modal {
		return
	}
	if d.store == nil {
		d.revision = ""
		d.status.SetText("Локальный список недоступен: нет каталога настроек.")
		d.controls()
		return
	}
	entries, rev, err := d.store.Load()
	if err != nil {
		d.revision = ""
		d.entries = nil
		d.render()
		d.status.SetText("Не удалось прочитать список; существующий файл не изменён.")
		return
	}
	d.entries = entries
	d.revision = rev
	d.render()
	d.status.SetText(fmt.Sprintf("Записей: %d. Проверьте актуальность адресов.", len(entries)))
}
func (d *devicesPanel) editor() server.SavedDevice {
	return server.SavedDevice{Name: strings.TrimSpace(d.name.Text), Host: strings.TrimSpace(d.host.Text), Note: strings.TrimSpace(d.note.Text)}
}
func (d *devicesPanel) save(next []server.SavedDevice, message string) {
	if d.modal || d.revision == "" || d.store == nil {
		return
	}
	rev, err := d.store.Save(d.revision, next)
	if err != nil {
		if errors.Is(err, server.ErrInvalidDevices) {
			d.status.SetText("Некорректный список: проверьте поля, уникальность названий и лимит 100 записей.")
			return
		}
		d.revision = ""
		d.controls()
		if errors.Is(err, server.ErrDeviceConflict) {
			d.status.SetText("Список изменился в другом окне. Обновите список и повторите.")
		} else {
			d.status.SetText("Не удалось сохранить список; результат может быть неизвестен. Обновите список.")
		}
		return
	}
	d.entries = next
	d.revision = rev
	d.render()
	d.status.SetText(message)
}
func (d *devicesPanel) importFile() {
	if d.modal || d.revision == "" {
		return
	}
	rev := d.revision
	d.modal = true
	d.modalSeq++
	token := d.modalSeq
	d.controls()
	d.open(func(r fyne.URIReadCloser, err error) {
		if !d.modal || d.modalSeq != token {
			if r != nil {
				_ = r.Close()
			}
			return
		}
		d.modal = false
		d.controls()
		if err != nil {
			if r != nil {
				_ = r.Close()
			}
			d.status.SetText("Не удалось открыть файл; импорт не выполнен.")
			return
		}
		if r == nil {
			return
		}
		data, err := io.ReadAll(io.LimitReader(r, 512*1024+1))
		closeErr := r.Close()
		if err != nil || closeErr != nil {
			d.status.SetText("Не удалось прочитать файл; импорт не выполнен.")
			return
		}
		items, err := server.DecodeDeviceExport(data)
		if err != nil {
			d.status.SetText("Некорректный файл или лишние поля; импорт не выполнен.")
			return
		}
		if d.revision != rev {
			d.status.SetText("Список изменился; обновите его перед импортом.")
			return
		}
		next := append([]server.SavedDevice{}, d.entries...)
		next = append(next, items...)
		d.save(next, fmt.Sprintf("Импорт завершён; добавлено записей: %d.", len(items)))
	})
}
func (d *devicesPanel) exportFile() {
	if d.modal || d.revision == "" {
		return
	}
	data, err := server.EncodeDeviceExport(d.entries)
	if err != nil {
		d.status.SetText("Не удалось подготовить экспорт.")
		return
	}
	d.modal = true
	d.modalSeq++
	token := d.modalSeq
	d.controls()
	d.saveFile(func(w fyne.URIWriteCloser, err error) {
		if !d.modal || d.modalSeq != token {
			if w != nil {
				w.Close()
			}
			return
		}
		d.modal = false
		d.controls()
		if err != nil {
			if w != nil {
				w.Close()
			}
			d.status.SetText("Не удалось открыть файл экспорта; он мог быть изменён.")
			return
		}
		if w == nil {
			return
		}
		n, writeErr := w.Write(data)
		closeErr := w.Close()
		if writeErr != nil || closeErr != nil || n != len(data) {
			d.status.SetText("Ошибка экспорта; файл может быть неполным.")
			return
		}
		d.status.SetText("Экспорт завершён. Файл не зашифрован и содержит адреса и заметки.")
	})
}
