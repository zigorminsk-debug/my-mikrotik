// Fyne setup for the separate, experimental Fyne desktop edition.
package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"unicode/utf16"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/zigorminsk-debug/my-mikrotik/fyneapp/internal/installer"
)

//go:embed payload/MikroTikInspectorFyne.exe
var payload []byte
var version = "0.4.1-fyne"

// The fixed script gets paths via environment variables, never script interpolation.
// Removal only removes a shortcut still pointing to our executable.
func shortcut(target string, remove bool) error {
	code := `$ErrorActionPreference='Stop'; $programs=[Environment]::GetFolderPath('Programs'); if (-not $programs) { throw 'No Start Menu folder' }; $path=Join-Path $programs 'MikroTik Inspector Fyne.lnk'; $shell=New-Object -ComObject WScript.Shell; if ($env:MTI_FYNE_REMOVE -eq '1') { if (Test-Path -LiteralPath $path) { $link=$shell.CreateShortcut($path); if ([String]::Equals($link.TargetPath,$env:MTI_FYNE_TARGET,[StringComparison]::OrdinalIgnoreCase)) { Remove-Item -LiteralPath $path } } } else { $link=$shell.CreateShortcut($path); $link.TargetPath=$env:MTI_FYNE_TARGET; $link.WorkingDirectory=[IO.Path]::GetDirectoryName($env:MTI_FYNE_TARGET); $link.Save() }`
	units := utf16.Encode([]rune(code))
	raw := make([]byte, 0, len(units)*2)
	for _, u := range units {
		raw = append(raw, byte(u), byte(u>>8))
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(raw))
	env := os.Environ()
	env = append(env, "MTI_FYNE_TARGET="+target)
	if remove {
		env = append(env, "MTI_FYNE_REMOVE=1")
	} else {
		env = append(env, "MTI_FYNE_REMOVE=0")
	}
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("не удалось обновить ярлык в меню Пуск: %w", err)
	}
	return nil
}

// Exercise the real embedded payload and managed install/remove logic in isolation.
func verifyPayload() error {
	if len(payload) < 2 || payload[0] != 'M' || payload[1] != 'Z' {
		return fmt.Errorf("payload is not an EXE")
	}
	dir, err := os.MkdirTemp("", "mti-fyne-check-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err = installer.Install(dir, payload); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, installer.Executable))
	if err != nil {
		return err
	}
	if len(data) != len(payload) {
		return fmt.Errorf("payload length mismatch")
	}
	if err = installer.Remove(dir); err != nil {
		return err
	}
	return nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--verify-payload" {
		if err := verifyPayload(); err != nil {
			os.Exit(2)
		}
		return
	}
	a := app.NewWithID("org.mikrotik.inspector.fyne.setup")
	w := a.NewWindow("Установка MikroTik Inspector Fyne " + version)
	w.Resize(fyne.NewSize(620, 450))
	base, err := os.UserCacheDir()
	if err != nil {
		dialog.ShowError(fmt.Errorf("не удалось определить папку текущего пользователя: %w", err), w)
		w.ShowAndRun()
		return
	}
	dir := filepath.Join(base, "Programs", "MikroTik Inspector Fyne")
	target := filepath.Join(dir, installer.Executable)
	title := widget.NewLabelWithStyle("MikroTik Inspector — Fyne", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	description := widget.NewLabel("Экспериментальная версия: подключение и диагностика. GeoNAT поддерживает применение с подтверждением и риском частичных изменений. Большая часть конструктора ещё не перенесена. Существующая версия не изменяется. Установка только для текущего пользователя, без администратора.")
	description.Wrapping = fyne.TextWrapWord
	location := widget.NewLabel("Папка: " + dir)
	location.Wrapping = fyne.TextWrapBreak
	status := widget.NewLabel("Закройте Fyne-приложение перед обновлением или удалением. Сохраните этот setup.exe: он также удаляет Fyne-версию. Настройки роутера не изменяются.")
	status.Wrapping = fyne.TextWrapWord
	var installButton, removeButton, openButton *widget.Button
	running := false
	busy := func(b bool) {
		running = b
		if b {
			installButton.Disable()
			removeButton.Disable()
			openButton.Disable()
		} else {
			installButton.Enable()
			removeButton.Enable()
			openButton.Enable()
		}
	}
	installButton = widget.NewButton("Установить / обновить", func() {
		dialog.ShowConfirm("Установка", "Установить отдельную экспериментальную Fyne-версию для текущего пользователя?", func(ok bool) {
			if !ok {
				return
			}
			busy(true)
			status.SetText("Установка…")
			go func() {
				e := installer.Install(dir, payload)
				if e == nil {
					e = shortcut(target, false)
					if e != nil {
						e = fmt.Errorf("приложение установлено, но ярлык не создан: %w", e)
					}
				}
				fyne.Do(func() {
					busy(false)
					if e != nil {
						status.SetText("Установка не завершена: " + e.Error())
						return
					}
					status.SetText("Готово. Приложение доступно в меню Пуск; существующая версия не изменена.")
				})
			}()
		}, w)
	})
	removeButton = widget.NewButton("Удалить Fyne-версию", func() {
		dialog.ShowConfirm("Удаление", "Удалить только управляемые файлы Fyne-версии и её ярлык? Другие файлы не удаляются.", func(ok bool) {
			if !ok {
				return
			}
			busy(true)
			status.SetText("Удаление…")
			go func() {
				e := installer.Remove(dir)
				if e == nil {
					e = shortcut(target, true)
					if e != nil {
						e = fmt.Errorf("приложение удалено, но ярлык остался: %w", e)
					}
				}
				fyne.Do(func() {
					busy(false)
					if e != nil {
						status.SetText("Удаление не завершено: " + e.Error())
						return
					}
					status.SetText("Приложение удалено. Прочие файлы и существующая версия не затронуты.")
				})
			}()
		}, w)
	})
	openButton = widget.NewButton("Открыть папку установки", func() {
		if e := exec.Command("explorer.exe", dir).Start(); e != nil {
			dialog.ShowError(e, w)
		}
	})
	w.SetCloseIntercept(func() {
		if running {
			status.SetText("Дождитесь окончания операции.")
			return
		}
		w.Close()
	})
	w.SetContent(container.NewVBox(title, description, widget.NewSeparator(), location, installButton, removeButton, openButton, widget.NewSeparator(), status))
	w.ShowAndRun()
}
