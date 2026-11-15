package viewmodel

import (
	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
	"strings"
	"testing"
)

func TestSnapshotStatusesAndPrivacyNotice(t *testing.T) {
	s := &mikrotik.NetworkSnapshot{Sections: map[string]mikrotik.SnapshotSection{"dhcpLeases": {Status: "ok", Rows: []map[string]string{{"host-name": "foo\nFAKE", "status": "bound"}}, Truncated: true}, "bridges": {Status: "error"}, "bridgePorts": {Status: "skipped"}}}
	o := Snapshot(s)
	for _, v := range []string{"foo FAKE", "усечён", "Ошибка чтения", "Не прочитано", "частные IP"} {
		if !strings.Contains(o, v) {
			t.Fatal(v, o)
		}
	}
	if strings.Contains(o, "foo\nFAKE") {
		t.Fatal(o)
	}
}
