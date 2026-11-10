package mikrotik

import (
	"strings"
	"testing"
	"time"
)

func TestBackupGenerate(t *testing.T) {
	device := &Info{RouterOS: "7.15", Packages: []Package{{Name: "routeros"}}}
	s := &Settings{Backup: BackupSettings{Enabled: true, Recipient: "admin@example.com", Password: `strong$pass"word`, Time: "02:30", Interval: "1d"}}
	result := Generate(device, nil, s, time.Now())
	if !result.OK {
		t.Fatal(result.Error)
	}
	for _, want := range []string{"mti-backup-schedule", "mti-scheduled.backup", "interval=1d", "02:30:00", "admin@example.com"} {
		if !strings.Contains(result.Script, want) {
			t.Errorf("missing %s", want)
		}
	}
	s.Backup.Password = "short"
	if Generate(device, nil, s, time.Now()).OK {
		t.Fatal("accepted short password")
	}
	s.Backup.Password = "abcdefghijkl"
	s.Backup.Time = "25:00"
	if Generate(device, nil, s, time.Now()).OK {
		t.Fatal("accepted invalid time")
	}
}
