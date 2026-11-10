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
