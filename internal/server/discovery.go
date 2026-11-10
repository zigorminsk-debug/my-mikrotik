package server

import (
	"encoding/binary"
	"net"
	"net/http"
	"strings"
	"time"
)

type neighbor struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Identity string `json:"identity"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Board    string `json:"board"`
}

// MNDP uses network byte order for TLV headers. Broadcast data is untrusted.
func parseMNDP(p []byte, ip net.IP) (neighbor, bool) {
	n := neighbor{}
	if len(p) < 14 || p[0] != 0 {
		return n, false
	}
	for off := 4; off+4 <= len(p); {
		typ, size := binary.BigEndian.Uint16(p[off:]), int(binary.BigEndian.Uint16(p[off+2:]))
		off += 4
		if size > len(p)-off {
			return neighbor{}, false
		}
		value := p[off : off+size]
		off += size
		switch typ {
		case 1:
			if size == 6 {
				n.MAC = strings.ToUpper(net.HardwareAddr(value).String())
			}
		case 5:
			n.Identity = string(value)
		case 7:
			n.Version = string(value)
		case 8:
			n.Platform = string(value)
		case 12:
			n.Board = string(value)
		}
	}
	if n.MAC == "" || n.MAC == "00:00:00:00:00:00" {
		return neighbor{}, false
	}
	if v := ip.To4(); v != nil && !v.IsUnspecified() && !v.IsLoopback() && !v.IsMulticast() {
		n.IP = v.String()
	}
	return n, true
}

var discoveryLock = make(chan struct{}, 1)

func handleDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	select {
	case discoveryLock <- struct{}{}:
		defer func() { <-discoveryLock }()
	default:
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "Поиск уже выполняется"})
		return
	}
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 5678})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Не удалось открыть UDP 5678: " + err.Error()})
		return
	}
	defer c.Close()
	targets := map[string]bool{"255.255.255.255": true}
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ip, nw, e := net.ParseCIDR(a.String())
			if e != nil || ip.To4() == nil {
				continue
			}
			v := ip.To4()
			mask := nw.Mask
			if len(mask) != 4 {
				continue
			}
			broadcast := net.IPv4(v[0]|^mask[0], v[1]|^mask[1], v[2]|^mask[2], v[3]|^mask[3])
			targets[broadcast.String()] = true
		}
	}
	sent := false
	for target := range targets {
		if _, err := c.WriteToUDP([]byte{0, 0, 0, 0}, &net.UDPAddr{IP: net.ParseIP(target), Port: 5678}); err == nil {
			sent = true
		}
	}
	if !sent {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Не удалось отправить широковещательный запрос"})
		return
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	found := []neighbor{}
	seen := map[string]bool{}
	for len(found) < 256 {
		count, addr, err := c.ReadFromUDP(buf)
		if err != nil {
			break
		}
		n, ok := parseMNDP(buf[:count], addr.IP)
		if ok && !seen[n.MAC] {
			found = append(found, n)
			seen[n.MAC] = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "neighbors": found})
}
