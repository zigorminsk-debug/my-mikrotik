package mikrotik

import (
	"bytes"
	"math/big"
	"net"
	"testing"
)

// Кривая EC-SRP: генератор на кривой и имеет порядок N.
func TestECCurveParams(t *testing.T) {
	// Gy^2 == Gx^3 + a*Gx + b (mod p).
	lhs := new(big.Int).Mod(new(big.Int).Mul(ecGy, ecGy), ecP)
	rhs := new(big.Int).Mul(ecGx, ecGx)
	rhs.Mul(rhs, ecGx)
	rhs.Add(rhs, new(big.Int).Mul(ecA, ecGx))
	rhs.Add(rhs, ecB)
	rhs.Mod(rhs, ecP)
	if lhs.Cmp(rhs) != 0 {
		t.Fatal("генератор не на кривой")
	}
	// N*G — точка в бесконечности.
	if !ecMul(ecOrder, ecG).inf {
		t.Fatal("N*G не является точкой в бесконечности")
	}
	// (N+1)*G == G.
	g2 := ecMul(new(big.Int).Add(ecOrder, big.NewInt(1)), ecG)
	if g2.inf || g2.x.Cmp(ecGx) != 0 || g2.y.Cmp(ecGy) != 0 {
		t.Fatal("(N+1)*G != G")
	}
}

func TestECKeygen(t *testing.T) {
	priv, pub, err := ecKeygen()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if priv.Sign() <= 0 {
		t.Fatal("приватный ключ не положительный")
	}
	// Публичный ключ: 32 байта Montgomery-X + байт чётности.
	if pub[32] > 1 {
		t.Fatalf("байт чётности = %d", pub[32])
	}
	// Точка из публичного ключа должна восстанавливаться.
	pt, ok := ecFromMont(new(big.Int).SetBytes(pub[:32]), pub[32])
	if !ok {
		t.Fatal("публичный ключ не восстанавливается в точку")
	}
	// priv*G должен совпасть с восстановленной точкой.
	want := ecMul(priv, ecG)
	if pt.x.Cmp(want.x) != 0 || pt.y.Cmp(want.y) != 0 {
		t.Fatal("восстановленная точка не совпала с priv*G")
	}
}

func TestMACHeaderRoundTrip(t *testing.T) {
	src := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	dst := net.HardwareAddr{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}
	hdr := encodeMACHeader(pktData, src, dst, 0x1234, 0xdeadbeef, false)
	if len(hdr) != macHeaderLen {
		t.Fatalf("длина заголовка = %d", len(hdr))
	}
	// Клиент→сервер: session key на 14, client type на 16.
	if hdr[14] != 0x12 || hdr[15] != 0x34 {
		t.Fatal("session key не на смещении 14")
	}
	if hdr[16] != 0x00 || hdr[17] != 0x15 {
		t.Fatal("client type не на смещении 16")
	}
	// Сервер→клиент: поля меняются местами.
	hdrS := encodeMACHeader(pktData, dst, src, 0x1234, 7, true)
	if hdrS[14] != 0x00 || hdrS[15] != 0x15 {
		t.Fatal("client type не на смещении 14 (fromServer)")
	}
	if hdrS[16] != 0x12 || hdrS[17] != 0x34 {
		t.Fatal("session key не на смещении 16 (fromServer)")
	}
	// Декодирование серверного пакета.
	h, ok := decodeMACHeader(hdrS)
	if !ok {
		t.Fatal("decode не удался")
	}
	if h.sessionKey != 0x1234 || h.counter != 7 || h.ptype != pktData {
		t.Fatalf("декодировано неверно: %+v", h)
	}
	if !macEqual(h.srcMAC, dst) || !macEqual(h.dstMAC, src) {
		t.Fatal("MAC-адреса не совпали")
	}
}

func TestCtrlBlockRoundTrip(t *testing.T) {
	payload := encodeCtrlBlock(ctrlUsername, []byte("admin"))
	payload = append(payload, encodeCtrlBlock(ctrlTermWidth, []byte{0x00, 0x50})...)
	blocks := parseCtrlBlocks(payload)
	if len(blocks) != 2 {
		t.Fatalf("блоков = %d", len(blocks))
	}
	if blocks[0].ctype != ctrlUsername || string(blocks[0].value) != "admin" {
		t.Fatalf("первый блок неверен: %+v", blocks[0])
	}
	if blocks[1].ctype != ctrlTermWidth || !bytes.Equal(blocks[1].value, []byte{0x00, 0x50}) {
		t.Fatalf("второй блок неверен: %+v", blocks[1])
	}
	// Хвост без magic — plain-данные терминала.
	blocks = parseCtrlBlocks([]byte("hello"))
	if len(blocks) != 1 || !blocks[0].plain || string(blocks[0].value) != "hello" {
		t.Fatalf("plain-блок неверен: %+v", blocks)
	}
}

func TestNormalizeMAC(t *testing.T) {
	p := Params{Host: "AA:BB:CC:DD:EE:FF", User: "admin"}
	if err := p.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.Protocol != "mac" {
		t.Fatalf("protocol = %q, ожидалось mac", p.Protocol)
	}
	if p.Host != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("host = %q", p.Host)
	}
	// MAC с числовым последним октетом не должен съедаться как порт.
	p2 := Params{Host: "aa:bb:cc:dd:ee:12", User: "admin"}
	if err := p2.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p2.Host != "aa:bb:cc:dd:ee:12" || p2.Protocol != "mac" {
		t.Fatalf("MAC с числовым октетом испорчен: host=%q proto=%q", p2.Host, p2.Protocol)
	}
	// Протокол mac с IP-адресом — ошибка.
	p3 := Params{Host: "192.168.88.1", User: "admin", Protocol: "mac"}
	if err := p3.normalize(); err == nil {
		t.Fatal("ожидалась ошибка для протокола mac с IP")
	}
}

func TestParseAsValue(t *testing.T) {
	out := "identity=MyRouter\r\nname=ether1;type=ether;mac-address=AA:BB:CC:DD:EE:FF;disabled=no\r\n"
	recs := parseAsValue(out)
	if len(recs) != 2 {
		t.Fatalf("записей = %d", len(recs))
	}
	if recs[0]["identity"] != "MyRouter" {
		t.Fatalf("identity = %q", recs[0]["identity"])
	}
	if recs[1]["name"] != "ether1" || recs[1]["disabled"] != "no" {
		t.Fatalf("интерфейс разобран неверно: %+v", recs[1])
	}
	// Эхо и промпт без '=' отфильтровываются.
	out2 := "[admin@MikroTik] > :put [/system identity print as-value]\r\nidentity=X\r\n"
	recs2 := parseAsValue(out2)
	if len(recs2) != 1 || recs2[0]["identity"] != "X" {
		t.Fatalf("эхо не отфильтровано: %+v", recs2)
	}
}

func TestCleanTerminalOutput(t *testing.T) {
	out := "[admin@MikroTik] > :put [/system identity print as-value]\r\nidentity=MyRouter\r\n[admin@MikroTik] > "
	got := cleanTerminalOutput(out, ":put [/system identity print as-value]")
	if got != "identity=MyRouter" {
		t.Fatalf("очистка неверна: %q", got)
	}
}
