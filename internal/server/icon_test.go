package server

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestIconICO(t *testing.T) {
	data := IconICO()
	if len(data) < 6+16 {
		t.Fatalf("ICO слишком короткий: %d байт", len(data))
	}
	if r := binary.LittleEndian.Uint16(data[0:2]); r != 0 {
		t.Errorf("зарезервированное поле = %d, ожидался 0", r)
	}
	if typ := binary.LittleEndian.Uint16(data[2:4]); typ != 1 {
		t.Errorf("тип = %d, ожидалась иконка (1)", typ)
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count == 0 {
		t.Fatal("в ICO нет ни одного изображения")
	}
	pngSig := []byte{0x89, 'P', 'N', 'G'}
	for i := 0; i < count; i++ {
		off := 6 + i*16
		entry := data[off : off+16]
		size := int(entry[0])
		if size == 0 {
			// В каталоге размер 256 кодируется нулём.
			size = 256
		}
		if h := int(entry[1]); h != int(entry[0]) {
			t.Errorf("размер %d: высота %d не совпадает с шириной", size, h)
		}
		length := binary.LittleEndian.Uint32(entry[8:12])
		start := binary.LittleEndian.Uint32(entry[12:16])
		if planes := binary.LittleEndian.Uint16(entry[4:6]); planes != 1 {
			t.Errorf("размер %d: planes = %d", size, planes)
		}
		if bpp := binary.LittleEndian.Uint16(entry[6:8]); bpp != 32 {
			t.Errorf("размер %d: бит на пиксель = %d", size, bpp)
		}
		if start+length > uint32(len(data)) {
			t.Fatalf("размер %d: данные выходят за пределы файла", size)
		}
		img := data[start : start+length]
		if !bytes.HasPrefix(img, pngSig) {
			t.Fatalf("размер %d: содержимое не PNG", size)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(img))
		if err != nil {
			t.Fatalf("размер %d: PNG не читается: %v", size, err)
		}
		if cfg.Width != size || cfg.Height != size {
			t.Errorf("в каталоге %d, а в PNG %dx%d", size, cfg.Width, cfg.Height)
		}
	}
}
