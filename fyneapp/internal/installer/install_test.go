package installer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallUpdateRemove(t *testing.T) {
	d := filepath.Join(t.TempDir(), "app")
	if err := Install(d, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "user.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Install(d, []byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := Remove(d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, Executable)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "user.txt")); err != nil {
		t.Fatal(err)
	}
}
func TestProtectUnknownOrModified(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, Executable)
	os.WriteFile(p, []byte("foreign"), 0600)
	if Install(d, []byte("payload")) == nil {
		t.Fatal("overwrote unknown")
	}
	if b, _ := os.ReadFile(p); string(b) != "foreign" {
		t.Fatal("changed foreign file")
	}
	os.Remove(p)
	if err := Install(d, []byte("known")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("modified"), 0600)
	if Remove(d) == nil || Install(d, []byte("new")) == nil {
		t.Fatal("modified file accepted")
	}
}
func TestRejectLock(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, lockName), nil, 0600)
	if Install(d, []byte("x")) == nil {
		t.Fatal("concurrent install accepted")
	}
}
