package mikrotik

import "testing"

func TestFilterLog(t *testing.T) {
	entries := []LogEntry{
		{Time: "1", Message: "mti: начинаю создание бэкапа"},
		{Time: "2", Message: "system,info,account user admin logged in"},
		{Time: "3", Message: "mti: письмо с бэкапом отправлено", Topics: "script,info"},
	}
	got := filterLog(entries, "mti", 10)
	if len(got) != 2 || got[0].Time != "1" || got[1].Time != "3" {
		t.Fatalf("ожидались только записи mti: %+v", got)
	}
	// Регистр не важен, ограничение по числу записей работает.
	got = filterLog(entries, "MTI", 1)
	if len(got) != 1 || got[0].Time != "3" {
		t.Fatalf("ожидалась последняя запись: %+v", got)
	}
	// Пустой фильтр — весь журнал.
	if got = filterLog(entries, "", 10); len(got) != 3 {
		t.Fatalf("ожидались все записи: %+v", got)
	}
}

func TestLogLimit(t *testing.T) {
	if got := logLimit(0); got != logDefaultLimit {
		t.Errorf("лимит по умолчанию = %d, ожидался %d", got, logDefaultLimit)
	}
	if got := logLimit(-5); got != logDefaultLimit {
		t.Errorf("отрицательный лимит = %d, ожидался %d", got, logDefaultLimit)
	}
	if got := logLimit(500); got != 500 {
		t.Errorf("лимит 500 изменён на %d", got)
	}
	if got := logLimit(5000); got != logMaxLimit {
		t.Errorf("слишком большой лимит = %d, ожидался %d", got, logMaxLimit)
	}
}
