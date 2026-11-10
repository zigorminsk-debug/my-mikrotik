package mikrotik

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
)

type BackupSettings struct {
	Enabled   bool   `json:"enabled"`
	Recipient string `json:"recipient"`
	Password  string `json:"password"`
	Time      string `json:"time"`
	Interval  string `json:"interval"`
}

func validateBackup(s BackupSettings) []string {
	var e []string
	a, err := mail.ParseAddress(s.Recipient)
	if err != nil || a.Address != s.Recipient || strings.ContainsAny(s.Recipient, "\r\n") {
		e = append(e, "Бэкап: укажите один адрес получателя")
	}
	if len(s.Password) < 12 || len(s.Password) > 128 || strings.ContainsAny(s.Password, "\r\n\x00") {
		e = append(e, "Бэкап: пароль должен содержать от 12 до 128 байт без переводов строк")
	}
	if !regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`).MatchString(s.Time) {
		e = append(e, "Бэкап: укажите время в формате ЧЧ:ММ")
	}
	if s.Interval != "1d" && s.Interval != "1w" {
		e = append(e, "Бэкап: выберите ежедневное или еженедельное расписание")
	}
	return e
}

func writeBackup(b *strings.Builder, s BackupSettings) {
	section(b, "System", "Зашифрованный бэкап по почте")
	b.WriteString("# SMTP настройте и проверьте заранее в /tool e-mail. Пароль бэкапа хранится в этом скрипте и на роутере.\n")
	// A fixed file prevents unbounded flash usage. Keep it after sending, even on failure.
	source := "/file remove [find where name=\"mti-scheduled.backup\"]; /system backup save name=\"mti-scheduled\" password=" + rosQuote(s.Password) + "; :delay 5s; /tool e-mail send to=" + rosQuote(s.Recipient) + " subject=(\"MikroTik backup: \" . [/system identity get name]) file=\"mti-scheduled.backup\""
	b.WriteString("/system scheduler remove [find where name=\"mti-backup-schedule\"]\n")
	b.WriteString("/system script remove [find where name=\"mti-backup-email\"]\n")
	fmt.Fprintf(b, "/system script add name=\"mti-backup-email\" policy=read,write,policy,test,sensitive source=%s\n", rosQuote(source))
	fmt.Fprintf(b, "/system scheduler add name=\"mti-backup-schedule\" start-time=%s interval=%s policy=read,write,policy,test,sensitive on-event=\"mti-backup-email\"\n", s.Time+":00", s.Interval)
}
