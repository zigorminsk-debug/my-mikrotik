package server

// Генерация иконок приложения без внешних зависимостей: PNG рисуется
// программно (суперсэмплинг 4×4), поэтому в репозитории нет бинарных
// ресурсов, а иконка одинакова в браузере и в Android-приложении.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

type rgb struct{ r, g, b float64 }

func (c rgb) mix(o rgb, t float64) rgb {
	return rgb{c.r + (o.r-c.r)*t, c.g + (o.g-c.g)*t, c.b + (o.b-c.b)*t}
}

var (
	colTop    = rgb{0x16, 0x33, 0x5c} // фон, верх
	colBottom = rgb{0x0f, 0x17, 0x2a} // фон, низ
	colAccent = rgb{0x38, 0xbd, 0xf8} // «сигнал» (голубой)
	colWhite  = rgb{0xf8, 0xfa, 0xfc} // корпус роутера
	colScreen = rgb{0x1d, 0x4e, 0xd8} // экранчик
)

// IconPNG рисует иконку заданного размера и возвращает PNG-байты.
func IconPNG(size int) []byte {
	if size <= 0 {
		size = 512
	}
	const ss = 4 // суперсэмплинг
	big := size * ss
	img := image.NewRGBA(image.Rect(0, 0, big, big))

	for y := 0; y < big; y++ {
		for x := 0; x < big; x++ {
			img.Set(x, y, iconPixel(x, y, big))
		}
	}

	out := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					c := img.RGBAAt(x*ss+dx, y*ss+dy)
					af := float64(c.A) / 255
					r += float64(c.R) * af
					g += float64(c.G) * af
					b += float64(c.B) * af
					a += af
				}
			}
			n := float64(ss * ss)
			if a <= 0 {
				out.SetRGBA(x, y, color.RGBA{0, 0, 0, 0})
				continue
			}
			out.SetRGBA(x, y, color.RGBA{
				R: uint8(clamp(r/a, 0, 255)),
				G: uint8(clamp(g/a, 0, 255)),
				B: uint8(clamp(b/a, 0, 255)),
				A: uint8(clamp(a/n*255, 0, 255)),
			})
		}
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	_ = enc.Encode(&buf, out)
	return buf.Bytes()
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// iconPixel считает цвет пикселя в координатах большого (сглаженного) полотна.
func iconPixel(x, y, n int) color.RGBA {
	s := float64(n)
	fx, fy := float64(x)/s, float64(y)/s // 0..1

	// Фон — скруглённый квадрат с вертикальным градиентом.
	if !inRoundRect(fx, fy, 0.0, 0.0, 1.0, 1.0, 0.22) {
		return color.RGBA{0, 0, 0, 0}
	}
	base := colTop.mix(colBottom, clamp(fy*1.1, 0, 1))

	// Мягкое голубое свечение сверху слева.
	dx, dy := fx-0.28, fy-0.22
	if d := dx*dx + dy*dy; d < 0.16 {
		base = base.mix(colAccent, 0.20*(1-d/0.16))
	}

	// Антенны.
	ant := rgb{0xcb, 0xd5, 0xe1}
	for _, ax := range []float64{0.34, 0.66} {
		if inRoundRect(fx, fy, ax-0.018, 0.30, 0.036, 0.22, 0.018) {
			base = ant
		}
		if dist(fx, fy, ax, 0.29) < 0.034 {
			base = colAccent
		}
	}

	// Корпус роутера.
	if inRoundRect(fx, fy, 0.20, 0.50, 0.60, 0.235, 0.075) {
		base = colWhite
		// Экранчик.
		if inRoundRect(fx, fy, 0.27, 0.565, 0.235, 0.095, 0.03) {
			base = colScreen
			// Три «индикатора» в экранчике.
			for i := 0; i < 3; i++ {
				cx := 0.305 + float64(i)*0.068
				if dist(fx, fy, cx, 0.612) < 0.019 {
					base = colAccent
				}
			}
		}
		// Три светодиода справа.
		for i := 0; i < 3; i++ {
			if dist(fx, fy, 0.635+float64(i)*0.052, 0.612) < 0.016 {
				base = colAccent.mix(colWhite, float64(i)*0.18)
			}
		}
	}

	// Волны Wi-Fi справа сверху.
	for i, rad := range []float64{0.085, 0.135, 0.185} {
		rr := dist(fx, fy, 0.80, 0.36)
		if rr > rad-0.017 && rr < rad+0.017 {
			// Дуга только в левой верхней четверти, чтобы не задевать антенны.
			if fx < 0.80 && fy < 0.36 {
				base = base.mix(colAccent, 0.85-float64(i)*0.18)
			}
		}
	}

	return color.RGBA{
		R: uint8(clamp(base.r, 0, 255)),
		G: uint8(clamp(base.g, 0, 255)),
		B: uint8(clamp(base.b, 0, 255)),
		A: 255,
	}
}

func dist(x, y, cx, cy float64) float64 {
	dx, dy := x-cx, y-cy
	return sqrt(dx*dx + dy*dy)
}

func sqrt(v float64) float64 {
	if v <= 0 {
		return 0
	}
	// Метод Ньютона: без импорта math ради одной функции.
	g := v
	for i := 0; i < 24; i++ {
		g = 0.5 * (g + v/g)
	}
	return g
}

// inRoundRect: точка внутри прямоугольника со скруглёнными углами.
func inRoundRect(x, y, rx, ry, w, h, r float64) bool {
	if x < rx || y < ry || x > rx+w || y > ry+h {
		return false
	}
	cx := clamp(x, rx+r, rx+w-r)
	cy := clamp(y, ry+r, ry+h-r)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}
