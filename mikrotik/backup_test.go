package mikrotik

import (
	"strings"
	"testing"
	"time"
)

func TestBackupGenerate(t *testing.T) {
	device := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	s := &Settings{Backup: BackupSettings{Enabled: true, From: "router@example.com", Recipient: "admin@example.com",
		SMTPServer: "mail.csl.by", SMTPPort: "587", SMTPUser: "mikrotik@csl.by", SMTPPass: `sm$tp"pass`,
		TLS: "starttls", Password: `strong$pass"word`, Time: "02:30", Interval: "1d"}}
	result := Generate(device, nil, s, time.Now())
	if !result.OK {
		t.Fatal(result.Error)
	}
	// Значения экранируются дважды: сначала для текста скрипта, затем для
	// командной строки, в которой этот скрипт создаётся.
	// Имя SMTP-сервера подставляется в скрипт строкой в кавычках.
	if !strings.Contains(result.Script, `:local smtpServer \"mail.csl.by\";`) {
		t.Errorf("имя сервера не в кавычках: %s", result.Script)
	}
	for _, want := range []string{
		"mti-backup-schedule", "mti-scheduled.backup", "interval=1d", "02:30:00",
		`admin@example.com`, `router@example.com`, `server=\$smtpServer`,
		"tls=starttls", "policy=ftp,read,write,policy,test,sensitive",
		`subject=(\"MikroTik backup: \"`, `:log info`,
		`password=\"strong\\\$pass\\\"word\"`,
	} {
		if !strings.Contains(result.Script, want) {
			t.Errorf("missing %s", want)
		}
	}
	// Имя сервера разбирается заранее, файл не отправляют до его появления.
	for _, want := range []string{`[:resolve \$smtpServer]`, ":delay 10s;", ":if (([:len [/file find where name="} {
		if !strings.Contains(result.Script, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(result.Script, "dont-encrypt") {
		t.Error("при заданном пароле бэкап не должен создаваться без шифрования")
	}
	// Без пароля шифрования бэкап создаётся открытым.
	s.Backup.Password = ""
	result = Generate(device, nil, s, time.Now())
	if !result.OK || !strings.Contains(result.Script, "dont-encrypt=yes") {
		t.Fatalf("ожидался бэкап без шифрования: %+v", result)
	}
}

func TestBackupSMTPValidation(t *testing.T) {
	device := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	good := BackupSettings{Enabled: true, From: "router@example.com", Recipient: "admin@example.com",
		SMTPServer: "mail.csl.by", SMTPPort: "587", TLS: "starttls", Time: "02:30", Interval: "1d"}
	if r := Generate(device, nil, &Settings{Backup: good}, time.Now()); !r.OK {
		t.Fatalf("корректные настройки не прошли: %s", r.Error)
	}
	bad := map[string]BackupSettings{
		"сервер":     {Enabled: true, From: "router@example.com", Recipient: "admin@example.com", SMTPPort: "587", Time: "02:30", Interval: "1d"},
		"порт":       {Enabled: true, From: "router@example.com", Recipient: "admin@example.com", SMTPServer: "mail.csl.by", SMTPPort: "0", Time: "02:30", Interval: "1d"},
		"отправител": {Enabled: true, From: "нет адреса", Recipient: "admin@example.com", SMTPServer: "mail.csl.by", SMTPPort: "587", Time: "02:30", Interval: "1d"},
		"получател":  {Enabled: true, From: "router@example.com", Recipient: "a@b, c@d", SMTPServer: "mail.csl.by", SMTPPort: "587", Time: "02:30", Interval: "1d"},
		"время":      {Enabled: true, From: "router@example.com", Recipient: "admin@example.com", SMTPServer: "mail.csl.by", SMTPPort: "587", Time: "25:00", Interval: "1d"},
		"период":     {Enabled: true, From: "router@example.com", Recipient: "admin@example.com", SMTPServer: "mail.csl.by", SMTPPort: "587", Time: "02:30", Interval: "1h"},
	}
	for name, b := range bad {
		if r := Generate(device, nil, &Settings{Backup: b}, time.Now()); r.OK {
			t.Errorf("ожидалась ошибка валидации: %s", name)
		}
	}
}

func TestBackupTLSByVersion(t *testing.T) {
	s := &Settings{Backup: BackupSettings{Enabled: true, From: "router@example.com", Recipient: "admin@example.com",
		SMTPServer: "mail.csl.by", SMTPPort: "587", TLS: "starttls", Time: "02:30", Interval: "1d"}}
	old := &Info{RouterOS: "6.49.7", Packages: []Package{{Name: "routeros"}}}
	newer := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	if r := Generate(old, nil, s, time.Now()); !r.OK || !strings.Contains(r.Script, "start-tls=yes") || strings.Contains(r.Script, "tls=starttls") {
		t.Fatalf("для RouterOS 6 ожидался start-tls: %+v", r)
	}
	if r := Generate(newer, nil, s, time.Now()); !r.OK || !strings.Contains(r.Script, "tls=starttls") || strings.Contains(r.Script, "start-tls=") {
		t.Fatalf("для RouterOS 7.15 ожидался tls=starttls: %+v", r)
	}
	s.Backup.TLS = "tls"
	if r := Generate(old, nil, s, time.Now()); r.OK {
		t.Fatal("полный TLS на RouterOS 6 должен отклоняться")
	}
	if r := Generate(newer, nil, s, time.Now()); !r.OK || !strings.Contains(r.Script, "tls=yes") {
		t.Fatalf("для RouterOS 7.15 ожидался tls=yes: %+v", r)
	}
	s.Backup.TLS = "no"
	if r := Generate(old, nil, s, time.Now()); !r.OK || !strings.Contains(r.Script, "start-tls=no") {
		t.Fatalf("ожидался start-tls=no: %+v", r)
	}
}
