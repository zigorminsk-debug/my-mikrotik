package server

import (
	"net"
	"testing"
)

func TestMNDP(t *testing.T) {
	packet := []byte{0, 1, 0, 0, 0, 1, 0, 6, 0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0, 5, 0, 3, 'a', 'b', 'c', 0, 12, 0, 2, 'R', 'B'}
	n, ok := parseMNDP(packet, net.ParseIP("192.168.88.1"))
	if !ok || n.MAC != "12:34:56:78:9A:BC" || n.Identity != "abc" || n.Board != "RB" || n.IP != "192.168.88.1" {
		t.Fatalf("unexpected: %#v %v", n, ok)
	}
	for _, p := range [][]byte{packet[:9], packet[:len(packet)-1], {0, 1, 0, 0, 0, 5, 0, 6, 'n', 'o', 'm', 'a', 'c', '!'}} {
		if _, ok := parseMNDP(p, net.IPv4zero); ok {
			t.Errorf("accepted bad packet %v", p)
		}
	}
}
