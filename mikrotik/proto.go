// Package mikrotik реализует клиент RouterOS API (бинарный протокол, TCP/8728)
// и REST API (HTTPS/443) на стандартной библиотеке Go — без внешних зависимостей.
package mikrotik

import (
	"fmt"
	"io"
	"strings"
)

// encodeLength кодирует длину слова в формате протокола RouterOS API:
//
//	< 0x80        — 1 байт
//	< 0x4000      — 2 байта, старший байт с маской 10xxxxxx
//	< 0x200000    — 3 байта, маска 110xxxxx
//	< 0x10000000  — 4 байта, маска 1110xxxx
func encodeLength(l int) ([]byte, error) {
	switch {
	case l < 0:
		return nil, fmt.Errorf("отрицательная длина слова: %d", l)
	case l < 0x80:
		return []byte{byte(l)}, nil
	case l < 0x4000:
		return []byte{byte(l>>8) | 0x80, byte(l)}, nil
	case l < 0x200000:
		return []byte{byte(l>>16) | 0xC0, byte(l >> 8), byte(l)}, nil
	case l < 0x10000000:
		return []byte{byte(l>>24) | 0xE0, byte(l >> 16), byte(l >> 8), byte(l)}, nil
	default:
		return nil, fmt.Errorf("слишком длинное слово: %d байт", l)
	}
}

// writeWord записывает одно слово с префиксом длины.
func writeWord(w io.Writer, s string) error {
	hdr, err := encodeLength(len(s))
	if err != nil {
		return err
	}
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if s == "" {
		return nil
	}
	_, err = io.WriteString(w, s)
	return err
}

// writeSentence записывает предложение: слова + нулевое слово-терминатор.
func writeSentence(w io.Writer, words ...string) error {
	for _, word := range words {
		if err := writeWord(w, word); err != nil {
			return err
		}
	}
	_, err := w.Write([]byte{0})
	return err
}

// readLength декодирует префикс длины слова.
func readLength(r io.Reader) (int, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	var extra, val int
	switch {
	case b[0]&0x80 == 0x00:
		return int(b[0]), nil
	case b[0]&0xC0 == 0x80:
		extra, val = 1, int(b[0]&0x3F)
	case b[0]&0xE0 == 0xC0:
		extra, val = 2, int(b[0]&0x1F)
	case b[0]&0xF0 == 0xE0:
		extra, val = 3, int(b[0]&0x0F)
	case b[0]&0xF8 == 0xF0:
		extra, val = 4, int(b[0]&0x07)
	default:
		return 0, fmt.Errorf("некорректный байт длины 0x%02x", b[0])
	}
	buf := make([]byte, extra)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, err
	}
	for _, c := range buf {
		val = val<<8 | int(c)
	}
	return val, nil
}

// readSentence читает предложение до нулевого слова-терминатора.
// Пустое слово в протоколе RouterOS всегда означает конец предложения.
func readSentence(r io.Reader) ([]string, error) {
	var words []string
	for {
		n, err := readLength(r)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return words, nil
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		words = append(words, string(buf))
	}
}

// splitAttr разбирает слово вида "=key=value".
func splitAttr(word string) (key, value string, ok bool) {
	if !strings.HasPrefix(word, "=") {
		return "", "", false
	}
	s := word[1:]
	if i := strings.IndexByte(s, '='); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", true
}

// attrs превращает список слов "=key=value" в карту.
func attrs(words []string) map[string]string {
	m := make(map[string]string, len(words))
	for _, w := range words {
		if k, v, ok := splitAttr(w); ok {
			m[k] = v
		}
	}
	return m
}
