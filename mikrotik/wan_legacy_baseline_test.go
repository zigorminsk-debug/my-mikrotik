package mikrotik

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

// Golden fingerprints capture the current output, not a claim about behavior
// on hardware. An intentional change requires review against the known-good
// RB951G configuration as well as failover tests.
func TestLegacyWANScriptsUnchanged(t *testing.T) {
	cases := []struct {
		mode    string
		enabled bool
		hash    string
	}{
		{"dhcp", false, "f4a44245d18a6953e6347837cc6815177b568731d00105e7269021e41294d368"},
		{"dhcp", true, "5eb8bef7dcc2d8a95a522ae32fbdfb756b59c33f8151fa710978702af83af5ec"},
		{"static", false, "570275e7dedb9f58babc5f9ccc31a25ac31d2e03ce7e4d44adb59cc63495c2f6"},
		{"static", true, "a22b5f8cbc807507b4d0f4b0d94c2f592d28128df6e8a834e1ebf938bcea0e9b"},
		{"lte", false, "83ec49620c7fa0efab132cbce1f24c19a8ea3a3ef37754a87816d8295659e0f0"},
		{"lte", true, "62167a4e08f48ed20fa0bfe34fb5b4ead1b2e0cd3b71c70677bdd8da1d003ce2"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s-%t", tc.mode, tc.enabled), func(t *testing.T) {
			s := WANSettings{Failover: tc.enabled, Links: []WANLink{{Mode: tc.mode, Interface: "wan1", Distance: 1, Gateway: "192.0.2.1", Address: "192.0.2.2/24"}, {Mode: "dhcp", Interface: "wan2", Distance: 2}}}
			var b strings.Builder
			buildWAN(&b, s, &Info{RouterOS: "7.9.2"}, []Iface{{Name: "wan1", Type: "lte"}, {Name: "wan2", Type: "ether"}})
			got := fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
			if got != tc.hash {
				t.Errorf("legacy WAN output changed: got %s; expected %s; review hardware impact", got, tc.hash)
			}
		})
	}
}
