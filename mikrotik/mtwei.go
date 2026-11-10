package mikrotik

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"
)

// Реализация EC-SRP5 (IEEE P1363.2) — аутентификации MAC-Telnet на RouterOS 7.x.
// Кривая задана над полем Curve25519 (p = 2^255-19); точки кодируются как
// 32 байта Montgomery-X (big-endian) + 1 байт чётности Y. Константы и порядок
// операций повторяют эталонный mtwei.c из haakonnessjoen/MAC-Telnet.

func mustHex(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("invalid EC constant: " + s)
	}
	return v
}

var (
	ecP     = mustHex("7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffed")
	ecA     = mustHex("2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa984914a144")
	ecB     = mustHex("7b425ed097b425ed097b425ed097b425ed097b425ed097b4260b5e9c7710c864")
	ecGx    = mustHex("2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad245a")
	ecGy    = mustHex("5f51e65e475f794b1fe122d388b72eb36dc2b28192839e4dd6163a5d81312c14")
	ecOrder = mustHex("1000000000000000000000000000000014def9dea2f79cd65812631a5cf5d3ed")
	ecW2M   = mustHex("555555555555555555555555555555555555555555555555555555555552db9c")
	ecM2W   = mustHex("2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad2451")
)

// ecG — генератор кривой.
var ecG = &ecPoint{x: new(big.Int).Set(ecGx), y: new(big.Int).Set(ecGy)}

// ecPoint — точка кривой в аффинных координатах; inf — точка в бесконечности.
type ecPoint struct {
	x, y *big.Int
	inf  bool
}

func bModP(x *big.Int) *big.Int { return new(big.Int).Mod(x, ecP) }

// ecAdd складывает две точки (результат — новая точка, входы не меняются).
func ecAdd(p, q *ecPoint) *ecPoint {
	if p.inf {
		return q
	}
	if q.inf {
		return p
	}
	var lam *big.Int
	if p.x.Cmp(q.x) == 0 && p.y.Cmp(q.y) == 0 {
		// Удвоение: lam = (3x^2 + a) / (2y). Точка с y=0 имеет порядок 2,
		// её удвоение — точка в бесконечности.
		if p.y.Sign() == 0 {
			return &ecPoint{inf: true}
		}
		num := new(big.Int).Mul(p.x, p.x)
		num.Mul(num, big.NewInt(3))
		num.Add(num, ecA)
		den := new(big.Int).Mul(p.y, big.NewInt(2))
		lam = new(big.Int).Mul(num, new(big.Int).ModInverse(den, ecP))
	} else {
		// Сложение: lam = (y2 - y1) / (x2 - x1). Случай p == -q даёт
		// нулевой знаменатель — он обработан ниже через inf.
		if p.x.Cmp(q.x) == 0 {
			return &ecPoint{inf: true}
		}
		num := new(big.Int).Sub(q.y, p.y)
		den := new(big.Int).Sub(q.x, p.x)
		lam = new(big.Int).Mul(num, new(big.Int).ModInverse(den, ecP))
	}
	lam.Mod(lam, ecP)
	x3 := new(big.Int).Mul(lam, lam)
	x3.Sub(x3, p.x)
	x3.Sub(x3, q.x)
	x3.Mod(x3, ecP)
	y3 := new(big.Int).Sub(p.x, x3)
	y3.Mul(y3, lam)
	y3.Sub(y3, p.y)
	y3.Mod(y3, ecP)
	return &ecPoint{x: x3, y: y3}
}

// ecMul умножает точку на скаляр (двойное сложение).
func ecMul(k *big.Int, p *ecPoint) *ecPoint {
	result := &ecPoint{inf: true}
	addend := p
	for i := 0; i < k.BitLen(); i++ {
		if k.Bit(i) == 1 {
			result = ecAdd(result, addend)
		}
		addend = ecAdd(addend, addend)
	}
	return result
}

// ecSqrt возвращает квадратный корень из n по модулю p (p % 8 == 5)
// или nil, если корня нет.
func ecSqrt(n *big.Int) *big.Int {
	n = new(big.Int).Mod(n, ecP)
	exp := new(big.Int).Add(ecP, big.NewInt(3))
	exp.Rsh(exp, 3) // (p+3)/8
	r := new(big.Int).Exp(n, exp, ecP)
	if new(big.Int).Mod(new(big.Int).Mul(r, r), ecP).Cmp(n) != 0 {
		exp2 := new(big.Int).Sub(ecP, big.NewInt(1))
		exp2.Rsh(exp2, 2) // (p-1)/4 — даёт sqrt(-1)
		r.Mul(r, new(big.Int).Exp(big.NewInt(2), exp2, ecP))
		r.Mod(r, ecP)
	}
	if new(big.Int).Mod(new(big.Int).Mul(r, r), ecP).Cmp(n) != 0 {
		return nil
	}
	return r
}

// pad32 записывает n как 32 байта big-endian (BN_bn2binpad).
func pad32(n *big.Int) []byte {
	b := n.Bytes()
	out := make([]byte, 32)
	if len(b) >= 32 {
		copy(out, b[len(b)-32:])
	} else {
		copy(out[32-len(b):], b)
	}
	return out
}

// montXBytes возвращает Montgomery-X координаты точки (32 байта BE).
func montXBytes(p *ecPoint) []byte {
	mx := new(big.Int).Add(p.x, ecW2M)
	mx.Mod(mx, ecP)
	return pad32(mx)
}

// ecFromMont строит точку по Montgomery-X и байту чётности Y.
// Возвращает ok=false, если точки с таким X на кривой нет.
func ecFromMont(montX *big.Int, parity byte) (*ecPoint, bool) {
	wx := new(big.Int).Add(montX, ecM2W)
	wx.Mod(wx, ecP)
	// y^2 = x^3 + a*x + b.
	y2 := new(big.Int).Mul(wx, wx)
	y2.Mul(y2, wx)
	t := new(big.Int).Mul(ecA, wx)
	y2.Add(y2, t)
	y2.Add(y2, ecB)
	y2.Mod(y2, ecP)
	y := ecSqrt(y2)
	if y == nil {
		return nil, false
	}
	if byte(y.Bit(0)) != parity {
		y = new(big.Int).Sub(ecP, y)
		y.Mod(y, ecP)
	}
	if byte(y.Bit(0)) != parity {
		return nil, false
	}
	return &ecPoint{x: wx, y: y}, true
}

// ecTangle (REDP1) подмешивает валидатор в точку target и возвращает
// новое значение target и сам валидатор как число. negate выбирает чётность
// Y выведенной точки (сервер использует 0, клиент при снятии «слепоты» — 1).
func ecTangle(target *ecPoint, validator []byte, negate bool) (*ecPoint, *big.Int) {
	v := new(big.Int).SetBytes(validator)
	vpt := ecMul(v, ecG)
	edpx := new(big.Int).SetBytes(sha256.Sum256(montXBytes(vpt))[:])
	parity := byte(0)
	if negate {
		parity = 1
	}
	for {
		edpxm := new(big.Int).SetBytes(sha256.Sum256(pad32(edpx))[:])
		if pt, ok := ecFromMont(edpxm, parity); ok {
			vpt = pt
			break
		}
		edpx.Add(edpx, big.NewInt(1))
	}
	return ecAdd(target, vpt), v
}

// ecKeygen генерирует ключевую пару клиента: приватный ключ и 33-байтный
// публичный ключ (32 байта Montgomery-X + байт чётности).
func ecKeygen() (priv *big.Int, pubkey [33]byte, err error) {
	privBytes := make([]byte, 32)
	if _, err := rand.Read(privBytes); err != nil {
		return nil, pubkey, err
	}
	privBytes[0] &= 248
	privBytes[31] &= 127
	privBytes[31] |= 64
	priv = new(big.Int).SetBytes(privBytes)
	pub := ecMul(priv, ecG)
	copy(pubkey[:32], montXBytes(pub))
	pubkey[32] = byte(pub.y.Bit(0))
	return priv, pubkey, nil
}

// ecValidator вычисляет валидатор EC-SRP: SHA256(salt || SHA256(user:pass)).
// Имя пользователя — до знака '+' (опции логина в валидатор не входят).
func ecValidator(username, password string, salt []byte) []byte {
	h1 := sha256.Sum256([]byte(username + ":" + password))
	h := sha256.New()
	h.Write(salt)
	h.Write(h1[:])
	return h.Sum(nil)
}

// ecProof вычисляет 32-байтный proof клиента по ключу сервера, своему ключу
// и валидатору (mtwei_docrypto).
func ecProof(priv *big.Int, serverKey, clientKey [33]byte, validator []byte) ([]byte, error) {
	serverPub, ok := ecFromMont(new(big.Int).SetBytes(serverKey[:32]), serverKey[32])
	if !ok {
		return nil, fmt.Errorf("некорректный публичный ключ сервера")
	}
	serverPub, v := ecTangle(serverPub, validator, true)
	h := sha256.New()
	h.Write(clientKey[:32])
	h.Write(serverKey[:32])
	hj := h.Sum(nil)
	vh := new(big.Int).Mul(v, new(big.Int).SetBytes(hj))
	vh.Add(vh, priv)
	vh.Mod(vh, ecOrder)
	z := montXBytes(ecMul(vh, serverPub))
	hf := sha256.New()
	hf.Write(hj)
	hf.Write(z)
	return hf.Sum(nil), nil
}

// md5Response вычисляет ответ legacy-аутентификации: 0x00 || MD5(0x00||pass||salt).
func md5Response(password string, salt []byte) []byte {
	h := md5.New()
	h.Write([]byte{0})
	h.Write([]byte(password))
	h.Write(salt)
	sum := h.Sum(nil)
	out := make([]byte, 17)
	copy(out[1:], sum)
	return out
}
