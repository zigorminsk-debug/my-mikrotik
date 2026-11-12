package mikrotik

import (
	"strings"
	"testing"
)

func TestTerminalCommandError(t *testing.T) {
	for _, s := range []string{
		"failure: can not run on slave interface", "syntax error (line 1 column 6)",
		"bad command name bridge", "not enough permissions", "input does not match any value of interface",
	} {
		if err := terminalCommandError(s); err == nil || !strings.Contains(err.Error(), s) {
			t.Errorf("terminal error not reported: %q: %v", s, err)
		}
	}
	if err := terminalCommandError("name=bridge;type=bridge\nname=ether1;type=ether"); err != nil {
		t.Fatalf("valid output rejected: %v", err)
	}
}
