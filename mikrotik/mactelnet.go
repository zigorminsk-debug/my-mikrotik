package mikrotik

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MAC-Telnet (L2-транспорт RouterOS): UDP-пакеты на порт 20561, внутри каждого —
// собственный заголовок с MAC-адресами отправителя и получателя. Клиент шлёт
// широковещательные пакеты с реальным MAC исходящего интерфейса; роутер
// отвечает на session key, поэтому сессия ведётся по broadcast-сокету.
// Реализация повторяет эталонный mactelnet (haakonnessjoen/MAC-Telnet).

const (
	macTelnetPort = 20561
	macHeaderLen  = 22
	macCtrlHdrLen = 9 // magic(4) + type(1) + length(4 BE)
	macClientType = 0x0015
	macMaxPacket  = 1500
)

// Типы пакетов MAC-Telnet (enum mt_ptype).
const (
	pktSessionStart = 0
	pktData         = 1
	pktAck          = 2
	pktPing         = 4
	pktPong         = 5
	pktEnd          = 255
)

// Типы control-блоков (enum mt_cptype).
const (
	ctrlBeginAuth   = 0
	ctrlPassSalt    = 1
	ctrlPassword    = 2
	ctrlUsername    = 3
	ctrlTermType    = 4
	ctrlTermWidth   = 5
	ctrlTermHeight  = 6
	ctrlPacketError = 7
	ctrlEndAuth     = 9
)

// Расписание ретрансмиссий из эталонного клиента.
var macRetransmitIntervals = []time.Duration{
	15 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond,
	50 * time.Millisecond, 90 * time.Millisecond, 170 * time.Millisecond,
	330 * time.Millisecond, 660 * time.Millisecond, 1000 * time.Millisecond,
}

var macCtrlMagic = []byte{0x56, 0x34, 0x12, 0xff}

// encodeMACHeader собирает 22-байтный заголовок. Поля session key и client type
// меняются местами в зависимости от направления (mt_direction_fromserver).
func encodeMACHeader(ptype byte, srcMAC, dstMAC net.HardwareAddr, sessionKey uint16, counter uint32, fromServer bool) []byte {
	buf := make([]byte, macHeaderLen)
	buf[0] = 1
	buf[1] = ptype
	copy(buf[2:8], srcMAC)
	copy(buf[8:14], dstMAC)
	if fromServer {
		binary.BigEndian.PutUint16(buf[14:16], macClientType)
		binary.BigEndian.PutUint16(buf[16:18], sessionKey)
	} else {
		binary.BigEndian.PutUint16(buf[14:16], sessionKey)
		binary.BigEndian.PutUint16(buf[16:18], macClientType)
	}
	binary.BigEndian.PutUint32(buf[18:22], counter)
	return buf
}

type macHeader struct {
	ptype      byte
	srcMAC     net.HardwareAddr
	dstMAC     net.HardwareAddr
	sessionKey uint16
	counter    uint32
	payload    []byte
}

// decodeMACHeader разбирает входящий пакет (всегда server→client, поэтому
// session key читается со смещения 16).
func decodeMACHeader(buf []byte) (*macHeader, bool) {
	if len(buf) < macHeaderLen || buf[0] != 1 {
		return nil, false
	}
	return &macHeader{
		ptype:      buf[1],
		srcMAC:     net.HardwareAddr(append(net.HardwareAddr(nil), buf[2:8]...)),
		dstMAC:     net.HardwareAddr(append(net.HardwareAddr(nil), buf[8:14]...)),
		sessionKey: binary.BigEndian.Uint16(buf[16:18]),
		counter:    binary.BigEndian.Uint32(buf[18:22]),
		payload:    buf[macHeaderLen:],
	}, true
}

// encodeCtrlBlock собирает control-блок: magic + type + length(BE) + value.
func encodeCtrlBlock(ctype byte, value []byte) []byte {
	buf := make([]byte, macCtrlHdrLen+len(value))
	copy(buf[0:4], macCtrlMagic)
	buf[4] = ctype
	binary.BigEndian.PutUint32(buf[5:9], uint32(len(value)))
	copy(buf[macCtrlHdrLen:], value)
	return buf
}

type ctrlBlock struct {
	ctype byte
	value []byte
	plain bool // сырые данные терминала (без magic)
}

// parseCtrlBlocks разбирает payload DATA-пакета на control-блоки; хвост без
// magic возвращается как один plain-блок (как parse_control_packet в эталоне).
func parseCtrlBlocks(payload []byte) []ctrlBlock {
	var blocks []ctrlBlock
	off := 0
	for off < len(payload) {
		rem := payload[off:]
		if len(rem) >= macCtrlHdrLen && bytes.Equal(rem[0:4], macCtrlMagic) {
			length := int(binary.BigEndian.Uint32(rem[5:9]))
			if length < 0 || length > len(rem)-macCtrlHdrLen {
				break
			}
			blocks = append(blocks, ctrlBlock{ctype: rem[4], value: rem[macCtrlHdrLen : macCtrlHdrLen+length]})
			off += macCtrlHdrLen + length
			continue
		}
		blocks = append(blocks, ctrlBlock{plain: true, value: rem})
		break
	}
	return blocks
}

// vt100Tracker следит за курсором виртуального терминала и отвечает на
// запросы роутера (DSR/DECID), без чего RouterOS считает терминал 1x1 и ломает
// вывод. Возвращает очищенный текст (escape-последовательности вырезаны).
type vt100Tracker struct {
	row, col       int
	width, height  int
	state          int // 0 — текст, 1 — после ESC, 2 — внутри CSI
	csi            []byte
}

func newVT100Tracker(width, height int) *vt100Tracker {
	return &vt100Tracker{row: 1, col: 1, width: width, height: height}
}

// feed обрабатывает байты вывода роутера; возвращает текст для приложения и
// ответы на запросы терминала (их нужно немедленно отправить роутеру).
func (t *vt100Tracker) feed(data []byte) (clean []byte, responses [][]byte) {
	for _, b := range data {
		switch t.state {
		case 0:
			switch {
			case b == 0x1b:
				t.state = 1
			case b == '\r':
				t.col = 1
				clean = append(clean, b)
			case b == '\n':
				t.row++
				if t.row > t.height {
					t.row = t.height
				}
				clean = append(clean, b)
			case b == '\b':
				if t.col > 1 {
					t.col--
				}
			case b == '\t':
				t.col = (t.col-1)/8*8 + 9
				if t.col > t.width {
					t.col = t.width
				}
				clean = append(clean, b)
			case b >= 0x20 && b < 0x7f:
				t.col++
				if t.col > t.width {
					t.col = t.width
				}
				clean = append(clean, b)
			}
		case 1:
			if b == '[' {
				t.state = 2
				t.csi = t.csi[:0]
			} else if b == 'Z' {
				// DECID — запрос атрибутов устройства.
				responses = append(responses, []byte("\x1b[?1;0c"))
				t.state = 0
			} else {
				t.state = 0
			}
		case 2:
			if b >= 0x40 && b <= 0x7e {
				if resp := t.handleCSI(b); resp != nil {
					responses = append(responses, resp)
				}
				t.state = 0
			} else if b >= 0x20 && b <= 0x3f {
				t.csi = append(t.csi, b)
			} else {
				t.state = 0
			}
		}
	}
	return clean, responses
}

func (t *vt100Tracker) handleCSI(final byte) []byte {
	params := parseCSIParams(t.csi)
	switch final {
	case 'n': // DSR — сообщить позицию курсора.
		if len(params) > 0 && params[0] == 6 {
			return []byte(fmt.Sprintf("\x1b[%d;%dR", t.row, t.col))
		}
	case 'C':
		t.col += csiParam(params, 0, 1)
		if t.col > t.width {
			t.col = t.width
		}
	case 'D':
		t.col -= csiParam(params, 0, 1)
		if t.col < 1 {
			t.col = 1
		}
	case 'A':
		t.row -= csiParam(params, 0, 1)
		if t.row < 1 {
			t.row = 1
		}
	case 'B':
		t.row += csiParam(params, 0, 1)
		if t.row > t.height {
			t.row = t.height
		}
	case 'H', 'f':
		t.row = csiParam(params, 0, 1)
		t.col = csiParam(params, 1, 1)
		t.clamp()
	case 'G':
		t.col = csiParam(params, 0, 1)
		t.clamp()
	case 'd':
		t.row = csiParam(params, 0, 1)
		t.clamp()
	}
	return nil
}

func (t *vt100Tracker) clamp() {
	if t.row < 1 {
		t.row = 1
	}
	if t.row > t.height {
		t.row = t.height
	}
	if t.col < 1 {
		t.col = 1
	}
	if t.col > t.width {
		t.col = t.width
	}
}

func parseCSIParams(b []byte) []int {
	s := strings.TrimLeft(string(b), "?<=>")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ";")
	out := make([]int, len(parts))
	for i, p := range parts {
		if p != "" {
			n, _ := strconv.Atoi(p)
			out[i] = n
		}
	}
	return out
}

func csiParam(params []int, idx, def int) int {
	if idx < len(params) && params[idx] != 0 {
		return params[idx]
	}
	return def
}

// macSession — активная сессия MAC-Telnet.
type macSession struct {
	conn       *net.UDPConn
	destAddr   *net.UDPAddr // broadcast-адрес интерфейса:порт MAC-Telnet
	localMAC   net.HardwareAddr
	remoteMAC  net.HardwareAddr
	sessionKey uint16

	mu        sync.Mutex
	outCounter uint32
	inCounter  int64
	lastAck    uint32
	ackNotify  chan struct{}

	passSaltCh chan []byte
	endAuthCh  chan struct{}
	errCh      chan error
	done       chan struct{}
	closeOnce  sync.Once

	vt *vt100Tracker

	dataMu  sync.Mutex
	dataBuf []byte
	dataCh  chan struct{}

	firstRx     chan struct{}
	firstRxOnce sync.Once
}

func (s *macSession) buildPacket(ptype byte, payload []byte, counter uint32) []byte {
	pkt := encodeMACHeader(ptype, s.localMAC, s.remoteMAC, s.sessionKey, counter, false)
	return append(pkt, payload...)
}

// sendPacket отправляет один пакет без ожидания ACK.
func (s *macSession) sendPacket(ptype byte, payload []byte, counter uint32) error {
	_, err := s.conn.WriteToUDP(s.buildPacket(ptype, payload, counter), s.destAddr)
	return err
}

// sendData отправляет DATA-пакет со счётчиком outCounter и сдвигает его.
func (s *macSession) sendData(payload []byte) error {
	s.mu.Lock()
	counter := s.outCounter
	s.outCounter += uint32(len(payload))
	s.mu.Unlock()
	return s.sendPacket(pktData, payload, counter)
}

// waitAck ждёт ACK со счётчиком не меньше expected.
func (s *macSession) waitAck(expected uint32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		if s.lastAck >= expected {
			s.mu.Unlock()
			return true
		}
		ch := s.ackNotify
		s.mu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ch:
			timer.Stop()
		case <-timer.C:
			return false
		case <-s.done:
			timer.Stop()
			return false
		}
	}
}

// sendReliable отправляет DATA-пакет и ждёт его ACK с ретрансмиссиями.
func (s *macSession) sendReliable(payload []byte) error {
	s.mu.Lock()
	counter := s.outCounter
	s.outCounter += uint32(len(payload))
	expected := counter + uint32(len(payload))
	s.mu.Unlock()
	pkt := s.buildPacket(pktData, payload, counter)
	for _, iv := range macRetransmitIntervals {
		if _, err := s.conn.WriteToUDP(pkt, s.destAddr); err != nil {
			return err
		}
		if s.waitAck(expected, iv) {
			return nil
		}
		select {
		case <-s.done:
			return fmt.Errorf("соединение закрыто")
		default:
		}
	}
	return fmt.Errorf("роутер не отвечает (тайм-аут)")
}

// readLoop читает входящие пакеты: шлёт ACK, отбрасывает дубликаты по
// кумулятивному счётчику и разбирает control-блоки.
func (s *macSession) readLoop() {
	buf := make([]byte, macMaxPacket)
	for {
		n, _, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		h, ok := decodeMACHeader(buf[:n])
		if !ok || h.sessionKey != s.sessionKey {
			continue
		}
		if !macEqual(h.srcMAC, s.remoteMAC) || !macEqual(h.dstMAC, s.localMAC) {
			continue
		}
		s.firstRxOnce.Do(func() { close(s.firstRx) })
		switch h.ptype {
		case pktData:
			// ACK шлётся до проверки на дубликат: голый ACK по счётчику
			// заставляет роутер пересылать пакет и ломает VT100-пробу.
			_ = s.sendPacket(pktAck, nil, h.counter+uint32(len(h.payload)))
			s.mu.Lock()
			dup := !(h.counter > uint32(s.inCounter) || (uint32(s.inCounter)-h.counter) > 65535)
			if !dup {
				s.inCounter = int64(h.counter)
			}
			s.mu.Unlock()
			if dup {
				continue
			}
			s.handlePayload(h.payload)
		case pktAck:
			s.mu.Lock()
			if h.counter > s.lastAck {
				s.lastAck = h.counter
			}
			close(s.ackNotify)
			s.ackNotify = make(chan struct{})
			s.mu.Unlock()
		case pktPing:
			_ = s.sendPacket(pktPong, nil, 0)
		case pktEnd:
			_ = s.sendPacket(pktEnd, nil, 0)
			s.fail(fmt.Errorf("роутер завершил соединение"))
			return
		}
	}
}

func (s *macSession) handlePayload(payload []byte) {
	for _, blk := range parseCtrlBlocks(payload) {
		switch {
		case blk.plain:
			clean, responses := s.vt.feed(blk.value)
			for _, resp := range responses {
				_ = s.sendData(resp)
			}
			if len(clean) > 0 {
				s.dataMu.Lock()
				s.dataBuf = append(s.dataBuf, clean...)
				close(s.dataCh)
				s.dataCh = make(chan struct{})
				s.dataMu.Unlock()
			}
		case blk.ctype == ctrlPassSalt:
			select {
			case s.passSaltCh <- blk.value:
			default:
			}
		case blk.ctype == ctrlEndAuth:
			select {
			case s.endAuthCh <- struct{}{}:
			default:
			}
		}
	}
}

func (s *macSession) fail(err error) {
	s.closeOnce.Do(func() {
		select {
		case s.errCh <- err:
		default:
		}
		close(s.done)
	})
}

// Close завершает сессию: шлёт END и закрывает сокет.
func (s *macSession) Close() {
	s.closeOnce.Do(func() {
		_ = s.sendPacket(pktEnd, nil, 0)
		close(s.done)
	})
	_ = s.conn.Close()
}

// readUntilPrompt читает вывод терминала, пока не встретит minPrompts промптов
// вида "] >" или не истечёт тайм-аут. Возвращает весь накопленный текст
// (включая эхо и промпты). Для ожидания логина достаточно одного промпта,
// для завершения команды — двух (эхо команды и промпт после выполнения).
func (s *macSession) readUntilPrompt(timeout time.Duration, minPrompts int) string {
	deadline := time.Now().Add(timeout)
	for {
		s.dataMu.Lock()
		out := string(s.dataBuf)
		ch := s.dataCh
		s.dataMu.Unlock()
		if strings.Count(out, "] >") >= minPrompts {
			return out
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return out
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ch:
			timer.Stop()
		case <-timer.C:
			s.dataMu.Lock()
			out = string(s.dataBuf)
			s.dataMu.Unlock()
			return out
		case <-s.done:
			timer.Stop()
			s.dataMu.Lock()
			out = string(s.dataBuf)
			s.dataMu.Unlock()
			return out
		case err := <-s.errCh:
			timer.Stop()
			_ = err
			s.dataMu.Lock()
			out = string(s.dataBuf)
			s.dataMu.Unlock()
			return out
		}
	}
}

// drainData забирает накопленный вывод терминала.
func (s *macSession) drainData() string {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	out := string(s.dataBuf)
	s.dataBuf = s.dataBuf[:0]
	return out
}

// execTerminal отправляет команду в терминал и возвращает её вывод
// (без эха команды и промпта).
func (s *macSession) execTerminal(cmd string, timeout time.Duration) (string, error) {
	s.drainData()
	if err := s.sendData([]byte(cmd + "\r")); err != nil {
		return "", err
	}
	out := s.readUntilPrompt(timeout, 2)
	return cleanTerminalOutput(out, cmd), nil
}

// cleanTerminalOutput вырезает из вывода терминала эхо команды и промпт,
// оставляя только результат выполнения.
func cleanTerminalOutput(out, cmd string) string {
	var lines []string
	for _, line := range strings.FieldsFunc(out, func(r rune) bool { return r == '\r' || r == '\n' }) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "] >") {
			continue
		}
		lines = append(lines, line)
	}
	// Эхо команды может прийти фрагментами без промпта — отбрасываем строки,
	// которые являются частью отправленной команды.
	if cmd != "" {
		filtered := lines[:0]
		for _, line := range lines {
			if strings.Contains(cmd, line) && len(line) >= 2 {
				continue
			}
			filtered = append(filtered, line)
		}
		lines = filtered
	}
	return strings.Join(lines, "\n")
}

func macEqual(a, b net.HardwareAddr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// dialMAC устанавливает сессию MAC-Telnet к роутеру по его MAC-адресу:
// перебирает сетевые интерфейсы, шлёт SESSIONSTART и ведёт аутентификацию.
func dialMAC(ctx context.Context, targetMAC net.HardwareAddr, user, password string) (*macSession, error) {
	sessionKey := uint16(0)
	kb := make([]byte, 2)
	_, _ = rand.Read(kb)
	sessionKey = binary.BigEndian.Uint16(kb)
	if sessionKey == 0 {
		sessionKey = 0x4d54
	}
	sourcePort := 1024 + int(binary.BigEndian.Uint16(kb))%1024

	priv, clientPub, err := ecKeygen()
	if err != nil {
		return nil, fmt.Errorf("генерация ключа EC-SRP: %w", err)
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("получение списка интерфейсов: %w", err)
	}

	var lastErr error
	for _, iface := range ifaces {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(iface.HardwareAddr) != 6 {
			continue
		}
		_, bcast, ok := ifaceIPv4Broadcast(iface)
		if !ok {
			continue
		}
		// Сокет на INADDR_ANY: только так доходят broadcast-ответы роутера
		// (как insockfd в эталоне). Исходящий интерфейс выбирается ядром по
		// directed broadcast адресату, поэтому source IP/MAC корректны.
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: sourcePort})
		if err != nil {
			continue
		}
		_ = conn.SetWriteBuffer(64 << 10)
		sess := &macSession{
			conn:       conn,
			destAddr:   &net.UDPAddr{IP: bcast, Port: macTelnetPort},
			localMAC:   iface.HardwareAddr,
			remoteMAC:  targetMAC,
			sessionKey: sessionKey,
			inCounter:  -1,
			ackNotify:  make(chan struct{}),
			passSaltCh: make(chan []byte, 1),
			endAuthCh:  make(chan struct{}, 1),
			errCh:      make(chan error, 1),
			done:       make(chan struct{}),
			vt:         newVT100Tracker(4096, 25),
			dataCh:     make(chan struct{}),
			firstRx:    make(chan struct{}),
		}
		go sess.readLoop()

		// SESSIONSTART на directed broadcast интерфейса.
		_ = sess.sendPacket(pktSessionStart, nil, 0)
		select {
		case <-sess.firstRx:
		case <-sess.done:
			lastErr = fmt.Errorf("соединение закрыто")
			sess.Close()
			continue
		case <-time.After(2 * time.Second):
			lastErr = fmt.Errorf("нет ответа от роутера")
			sess.Close()
			continue
		case <-ctx.Done():
			sess.Close()
			return nil, ctx.Err()
		}

		if err := sess.handshake(user, password, priv, clientPub); err != nil {
			lastErr = err
			sess.Close()
			continue
		}
		return sess, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("MAC-подключение к %s: %w", targetMAC, lastErr)
	}
	return nil, fmt.Errorf("MAC-подключение к %s: нет подходящих сетевых интерфейсов (нужен интерфейс с IPv4 в том же L2-сегменте)", targetMAC)
}

// ifaceIPv4Broadcast возвращает IPv4-адрес интерфейса и его directed broadcast.
func ifaceIPv4Broadcast(iface net.Interface) (net.IP, net.IP, bool) {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, nil, false
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil || ip4.IsLoopback() || ip4.IsUnspecified() {
			continue
		}
		mask := ipnet.Mask
		if len(mask) != 4 {
			continue
		}
		bcast := net.IPv4(ip4[0]|^mask[0], ip4[1]|^mask[1], ip4[2]|^mask[2], ip4[3]|^mask[3])
		return ip4, bcast, true
	}
	return nil, nil, false
}

// handshake ведёт аутентификацию: BEGINAUTH+PASSSALT → PASSSALT сервера →
// PASSWORD+USERNAME+TERM_* → END_AUTH.
func (s *macSession) handshake(user, password string, priv *big.Int, clientPub [33]byte) error {
	// Предложение EC-SRP: BEGINAUTH (пустой) + PASSSALT(username\0 + pubkey).
	offer := make([]byte, 0, len(user)+1+33)
	offer = append(offer, user...)
	offer = append(offer, 0)
	offer = append(offer, clientPub[:]...)
	payload := encodeCtrlBlock(ctrlBeginAuth, nil)
	payload = append(payload, encodeCtrlBlock(ctrlPassSalt, offer)...)
	if err := s.sendReliable(payload); err != nil {
		return fmt.Errorf("отправка BEGINAUTH: %w", err)
	}

	// Ответ сервера: PASSSALT 49B (EC-SRP) или 16B (legacy MD5).
	var saltResp []byte
	select {
	case saltResp = <-s.passSaltCh:
	case err := <-s.errCh:
		return err
	case <-s.done:
		return fmt.Errorf("соединение закрыто до аутентификации")
	case <-time.After(10 * time.Second):
		return fmt.Errorf("роутер не прислал данные аутентификации")
	}

	var authValue []byte
	if len(saltResp) == 49 {
		// EC-SRP: server_pubkey(33) + salt(16).
		var serverKey [33]byte
		copy(serverKey[:], saltResp[:33])
		salt := saltResp[33:49]
		validator := ecValidator(loginBaseName(user), password, salt)
		proof, err := ecProof(priv, serverKey, clientPub, validator)
		if err != nil {
			return err
		}
		authValue = proof
	} else if len(saltResp) == 16 {
		authValue = md5Response(password, saltResp)
	} else {
		return fmt.Errorf("некорректная длина данных аутентификации: %d", len(saltResp))
	}

	// PASSWORD + USERNAME + TERM_TYPE + TERM_WIDTH + TERM_HEIGHT.
	auth := encodeCtrlBlock(ctrlPassword, authValue)
	auth = append(auth, encodeCtrlBlock(ctrlUsername, []byte(user))...)
	auth = append(auth, encodeCtrlBlock(ctrlTermType, []byte("vt100"))...)
	width := make([]byte, 2)
	binary.LittleEndian.PutUint16(width, 4096)
	auth = append(auth, encodeCtrlBlock(ctrlTermWidth, width)...)
	height := make([]byte, 2)
	binary.LittleEndian.PutUint16(height, 25)
	auth = append(auth, encodeCtrlBlock(ctrlTermHeight, height)...)
	if err := s.sendReliable(auth); err != nil {
		return fmt.Errorf("отправка пароля: %w", err)
	}

	select {
	case <-s.endAuthCh:
	case err := <-s.errCh:
		return err
	case <-s.done:
		return fmt.Errorf("соединение закрыто при аутентификации")
	case <-time.After(10 * time.Second):
		return fmt.Errorf("роутер не завершил аутентификацию")
	}

	// Ждём промпта терминала (END_AUTH сам по себе не означает успех).
	out := s.readUntilPrompt(10*time.Second, 1)
	if !strings.Contains(out, "] >") {
		if strings.Contains(out, "Login failed") || strings.Contains(out, "incorrect") {
			return fmt.Errorf("неверный логин или пароль")
		}
		return fmt.Errorf("не удалось войти в терминал роутера")
	}
	return nil
}

// loginBaseName возвращает имя аккаунта без опций логина (admin+t → admin).
func loginBaseName(user string) string {
	if i := strings.IndexByte(user, '+'); i >= 0 {
		return user[:i]
	}
	return user
}


