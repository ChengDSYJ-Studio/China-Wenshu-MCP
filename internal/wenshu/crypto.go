package wenshu

import (
	"crypto/cipher"
	"crypto/des"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func randomString(n int) (string, error) {
	const chars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b), nil
}

// cipherDate deliberately follows the site's strToBinary.js, including its
// zero-based month in October–December. CryptoJS zero-fills a short CBC IV.
func cipherDate(t time.Time) string {
	m := int(t.Month())
	if m < 10 {
		return t.Format("20060102")
	}
	return fmt.Sprintf("%04d%d%02d", t.Year(), m-1, t.Day())
}
func encrypt3DES(plain, key, iv []byte) ([]byte, error) {
	b, e := des.NewTripleDESCipher(key)
	if e != nil {
		return nil, e
	}
	v := make([]byte, 8)
	copy(v, iv)
	n := 8 - len(plain)%8
	p := append([]byte{}, plain...)
	for range n {
		p = append(p, byte(n))
	}
	out := make([]byte, len(p))
	cipher.NewCBCEncrypter(b, v).CryptBlocks(out, p)
	return out, nil
}
func GenerateCiphertext(t time.Time) (string, error) {
	salt, e := randomString(24)
	if e != nil {
		return "", e
	}
	return ciphertextWithSalt(t, salt)
}
func ciphertextWithSalt(t time.Time, salt string) (string, error) {
	iv := cipherDate(t.In(Shanghai))
	enc, e := encrypt3DES([]byte(strconv.FormatInt(t.UnixMilli(), 10)), []byte(salt), []byte(iv))
	if e != nil {
		return "", e
	}
	packet := salt + iv + base64.StdEncoding.EncodeToString(enc)
	bits := make([]string, len(packet))
	for i, c := range []byte(packet) {
		bits[i] = strconv.FormatInt(int64(c), 2)
	}
	return strings.Join(bits, " "), nil
}
func decryptResult(result json.RawMessage, key string, t time.Time) (json.RawMessage, error) {
	if key == "" {
		if !json.Valid(result) {
			return nil, fmt.Errorf("响应 result 不是 JSON")
		}
		return result, nil
	}
	var encoded string
	if e := json.Unmarshal(result, &encoded); e != nil {
		return nil, fmt.Errorf("响应 result 类型错误: %w", e)
	}
	data, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil {
		return nil, fmt.Errorf("响应 base64: %w", e)
	}
	if len(data) == 0 || len(data)%8 != 0 {
		return nil, fmt.Errorf("响应密文长度错误")
	}
	block, e := des.NewTripleDESCipher([]byte(key))
	if e != nil {
		return nil, e
	}
	for _, d := range []time.Time{t, t.AddDate(0, 0, -1), t.AddDate(0, 0, 1)} {
		out := make([]byte, len(data))
		cipher.NewCBCDecrypter(block, []byte(d.In(Shanghai).Format("20060102"))).CryptBlocks(out, data)
		pad := int(out[len(out)-1])
		if pad < 1 || pad > 8 {
			continue
		}
		valid := true
		for _, v := range out[len(out)-pad:] {
			if int(v) != pad {
				valid = false
			}
		}
		out = out[:len(out)-pad]
		if valid && json.Valid(out) {
			return out, nil
		}
	}
	return nil, fmt.Errorf("响应解密失败：检查系统时间或网站协议变更")
}
