package auth

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestEncryptSecretRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	encodedKey := base64.StdEncoding.EncodeToString(key)
	encrypted, err := EncryptSecret(encodedKey, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == "JBSWY3DPEHPK3PXP" {
		t.Fatal("secret was not encrypted")
	}
	plaintext, err := DecryptSecret(encodedKey, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("got %q", plaintext)
	}
}

func TestDecryptRejectsInvalidKey(t *testing.T) {
	if _, err := DecryptSecret("short", "v1:AAAA"); err == nil {
		t.Fatal("expected invalid key error")
	}
}
