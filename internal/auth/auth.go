package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

func RandomToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func encryptionKey(value string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	}
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be a base64-encoded 32-byte value")
	}
	return key, nil
}

func EncryptSecret(keyValue, plaintext string) (string, error) {
	key, err := encryptionKey(keyValue)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), []byte("shiden-guardian:totp:v1"))
	return "v1:" + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func DecryptSecret(keyValue, encoded string) (string, error) {
	if !strings.HasPrefix(encoded, "v1:") {
		return "", fmt.Errorf("unsupported encrypted secret format")
	}
	key, err := encryptionKey(keyValue)
	if err != nil {
		return "", err
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, "v1:"))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("encrypted secret is truncated")
	}
	plaintext, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte("shiden-guardian:totp:v1"))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	expected, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func GenerateTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}
func TOTPURI(secret, account string) string {
	return "otpauth://totp/Shiden%20Guardian:" + account + "?secret=" + secret + "&issuer=Shiden%20Guardian&algorithm=SHA1&digits=6&period=30"
}
func ValidateTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	for offset := -1; offset <= 1; offset++ {
		if subtle.ConstantTimeCompare([]byte(totp(secret, now.Add(time.Duration(offset)*30*time.Second))), []byte(code)) == 1 {
			return true
		}
	}
	return false
}
func totp(secret string, now time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return ""
	}
	counter := uint64(now.Unix() / 30)
	msg := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		msg[i] = byte(counter)
		counter >>= 8
	}
	mac := hmac.New(sha1.New, key)
	mac.Write(msg)
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0xf
	binary := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%06d", binary%1_000_000)
}

func RecoveryCodes(count int) ([]string, []string, error) {
	plain := make([]string, 0, count)
	hashes := make([]string, 0, count)
	for i := 0; i < count; i++ {
		token, err := RandomToken(8)
		if err != nil {
			return nil, nil, err
		}
		token = strings.ToUpper(token[:10])
		plain = append(plain, token)
		hashes = append(hashes, HashToken(token))
	}
	return plain, hashes, nil
}
func ParseCode(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return fmt.Sprintf("%06d", int(v))
	default:
		return ""
	}
}
func ValidUsername(v string) bool {
	if len(v) < 3 || len(v) > 40 {
		return false
	}
	for _, r := range v {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func ValidPassword(v string) bool {
	if len(v) < 14 || len(v) > 256 {
		return false
	}
	classes := 0
	for _, tests := range []string{"abcdefghijklmnopqrstuvwxyz", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "0123456789", "!@#$%^&*()_+-=[]{}:,.?"} {
		if strings.ContainsAny(v, tests) {
			classes++
		}
	}
	return classes >= 3
}
func IntCode(v string) int { n, _ := strconv.Atoi(v); return n }
