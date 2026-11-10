package mikrotik

import (
	"fmt"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
)

// Фиксированное имя файла: бэкапы не копятся на флеше.
const backupFileName = "mti-scheduled"

// Права скрипта и планировщика. ftp нужен для отправки файла письмом: без него
// /tool e-mail send file= падает, не оставляя записи в журнале. sensitive
// позволяет хранить пароль SMTP в тексте скрипта.
const backupPolicy = "ftp,read,write,policy,test,sensitive"

// BackupSettings — бэкап по расписанию с отправкой на почту. Настройки SMTP
// передаются прямо в команду отправки, поэтому /tool e-mail на роутере
// настраивать не нужно. TLS: "no", "starttls" или "tls" — полный TLS (SMTPS,
// обычно порт 465) доступен с RouterOS 7.13. Пустой пароль шифрования —
// бэкап без шифрования (dont-encrypt=yes).
type BackupSettings struct {
	Enabled    bool   `json:"enabled"`
	From       string `json:"from"`
	Recipient  string `json:"recipient"`
	SMTPServer string `json:"smtpServer"`
	SMTPPort   string `json:"smtpPort"`
	SMTPUser   string `json:"smtpUser"`
	SMTPPass   string `json:"smtpPass"`
	TLS        string `json:"tls"`
	Password   string `json:"password"`
	Time       string `json:"time"`
	Interval   string `json:"interval"`
}

// smtpTLS возвращает режим шифрования SMTP; пустое значение — STARTTLS.
func smtpTLS(s BackupSettings) string {
	switch s.TLS {
	case "no", "tls":
		return s.TLS
	}
	return "starttls"
}

// rosAtLeast сообщает, что версия RouterOS не ниже указанной. Неразобранная
// версия считается современной: приложение ориентируется на RouterOS 7.
func rosAtLeast(v string, major, minor int) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return true
	}
	maj, errMaj := strconv.Atoi(parts[0])
	min, errMin := strconv.Atoi(parts[1])
	if errMaj != nil || errMin != nil {
		return true
	}
	if maj != major {
		return maj > major
	}
	return min >= minor
}

// eMailTLSParam — параметр шифрования для /tool e-mail send. До RouterOS 7.13
// использовался start-tls=, начиная с 7.13 — tls=.
func eMailTLSParam(rosVersion, mode string) string {
	if rosAtLeast(rosVersion, 7, 13) {
		switch mode {
		case "no":
			return "tls=no"
		case "tls":
			return "tls=yes"
		default:
			return "tls=starttls"
		}
	}
	if mode == "no" {
		return "start-tls=no"
	}
	return "start-tls=yes"
}

// rosString — строковый литерал для текста скрипта, всегда в кавычках: имя
// SMTP-сервера и другие значения безопаснее передавать явной строкой.
func rosString(v string) string {
	q := rosQuote(v)
	if strings.HasPrefix(q, "\"") {
		return q
	}
	return "\"" + v + "\""
}

func validateBackup(s BackupSettings, rosVersion string) []string {
	var e []string
	if a, err := mail.ParseAddress(s.Recipient); err != nil || a.Address != s.Recipient || strings.ContainsAny(s.Recipient, "\r\n") {
		e = append(e, "Бэкап: укажите один адрес получателя")
	}
	if a, err := mail.ParseAddress(s.From); err != nil || a.Address != s.From || strings.ContainsAny(s.From, "\r\n") {
		e = append(e, "Бэкап: укажите один адрес отправителя (From)")
	}
	if !validHost(s.SMTPServer) {
		e = append(e, "Бэкап: укажите SMTP-сервер — DNS-имя или IP")
	}
	if !validPort(s.SMTPPort) {
		e = append(e, "Бэкап: некорректный порт SMTP")
	}
	if len(s.SMTPUser) > 128 || strings.ContainsAny(s.SMTPUser, "\r\n\x00") {
		e = append(e, "Бэкап: логин SMTP слишком длинный или содержит недопустимые символы")
	}
	if strings.ContainsAny(s.SMTPPass, "\r\n\x00") {
		e = append(e, "Бэкап: пароль SMTP содержит недопустимые символы")
	}
	if s.TLS != "" && s.TLS != "no" && s.TLS != "starttls" && s.TLS != "tls" {
		e = append(e, "Бэкап: выберите режим шифрования SMTP")
	}
	if s.TLS == "tls" && !rosAtLeast(rosVersion, 7, 13) {
		e = append(e, "Бэкап: полный TLS (SMTPS) требует RouterOS 7.13 или новее — выберите STARTTLS")
	}
	if s.Password != "" && (len(s.Password) < 12 || len(s.Password) > 128 || strings.ContainsAny(s.Password, "\r\n\x00")) {
		e = append(e, "Бэкап: пароль шифрования должен содержать от 12 до 128 байт без переводов строк")
	}
	if !regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`).MatchString(s.Time) {
		e = append(e, "Бэкап: укажите время в формате ЧЧ:ММ")
	}
	if s.Interval != "1d" && s.Interval != "1w" {
		e = append(e, "Бэкап: выберите ежедневное или еженедельное расписание")
	}
	return e
}

func writeBackup(b *strings.Builder, s BackupSettings, rosVersion string) {
	section(b, "System", "Бэкап по расписанию на почту")
	b.WriteString("# SMTP-сервер, порт, логин и пароль передаются самой команде отправки —\n")
	b.WriteString("# настраивать /tool e-mail на роутере не нужно.\n")
	b.WriteString("# Каждый шаг пишет в журнал: /log print where message~\"mti\".\n")
	b.WriteString("# Пароль SMTP и пароль шифрования видны в тексте скрипта — не публикуйте его.\n")
	b.WriteString("/system scheduler remove [find where name=\"mti-backup-schedule\"]\n")
	b.WriteString("/system script remove [find where name=\"mti-backup-email\"]\n")
	fmt.Fprintf(b, "/system script add name=\"mti-backup-email\" policy=%s source=%s\n",
		backupPolicy, rosQuote(backupScriptSource(s, rosVersion)))
	fmt.Fprintf(b, "/system scheduler add name=\"mti-backup-schedule\" start-time=%s interval=%s policy=%s on-event=\"mti-backup-email\"\n",
		s.Time+":00", s.Interval, backupPolicy)
}

// backupScriptSource — тело скрипта, которое выполняет планировщик.
func backupScriptSource(s BackupSettings, rosVersion string) string {
	file := backupFileName + ".backup"
	find := "[:len [/file find where name=\"" + file + "\"]]"
	save := "/system backup save name=\"" + backupFileName + "\""
	if s.Password == "" {
		save += " dont-encrypt=yes"
	} else {
		save += " password=" + rosQuote(s.Password)
	}
	send := []string{
		attr("to", s.Recipient),
		attr("from", s.From),
		"server=$smtpServer",
		attr("port", s.SMTPPort),
	}
	if strings.TrimSpace(s.SMTPUser) != "" {
		send = append(send, attr("user", s.SMTPUser))
		if s.SMTPPass != "" {
			send = append(send, attr("password", s.SMTPPass))
		}
	}
	send = append(send,
		eMailTLSParam(rosVersion, smtpTLS(s)),
		"file=\""+file+"\"",
		`subject=("MikroTik backup: " . [/system identity get name] . " " . [/system clock get date])`,
		`body=("Резервная копия MikroTik Inspector. Роутер: " . [/system identity get name] . ", создана: " . [/system clock get date] . " " . [/system clock get time])`,
	)
	lines := []string{
		`:log info "mti: начинаю создание бэкапа";`,
		// Имя SMTP-сервера разбираем заранее: /tool e-mail в RouterOS 7 часто
		// не может разобрать имя сам и письмо просто не уходит.
		":local smtpServer " + rosString(s.SMTPServer) + ";",
		`:do { :set smtpServer [:resolve $smtpServer] } on-error={ :log warning "mti: имя SMTP-сервера не разобрано — отправляю как есть" };`,
		`:do { /file remove [find where name="` + file + `"] } on-error={ :log warning "mti: старый файл бэкапа не удалён" };`,
		":do { " + save + ` } on-error={ :log error "mti: /system backup save завершился с ошибкой" };`,
		// Создание бэкапа идёт в фоне: ждём появления файла, иначе отправка
		// упадёт с «input does not match any value of file».
		":delay 10s;",
		":if ((" + find + ") = 0) do={ :delay 20s };",
		":if ((" + find + ") = 0) do={ :delay 30s };",
		`:if ((` + find + `) = 0) do={ :log error "mti: файл ` + file + ` не создан — письмо не отправлено" } else={`,
		":delay 2s;",
		`:do { /tool e-mail send ` + strings.Join(send, " ") + `; :log info "mti: письмо с бэкапом отправлено" } on-error={ :log error "mti: письмо не отправлено — проверьте SMTP-сервер, порт, TLS, логин и размер вложения" }`,
		"};",
		`:log info "mti: задание бэкапа завершено"`,
	}
	return strings.Join(lines, "\n")
}
