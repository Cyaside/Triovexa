package secretstore

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCipherPersistsKeyAndRoundTripsCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.key")
	first, err := NewCipher(path, "")
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := first.Encrypt("provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, "provider-secret") {
		t.Fatal("ciphertext exposed the plaintext credential")
	}
	second, err := NewCipher(path, "")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := second.Decrypt(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "provider-secret" {
		t.Fatalf("plaintext = %q", plaintext)
	}
}

func TestCipherRejectsWrongKey(t *testing.T) {
	first, err := NewCipher(filepath.Join(t.TempDir(), "first.key"), "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCipher(filepath.Join(t.TempDir(), "second.key"), "")
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := first.Encrypt("provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Decrypt(ciphertext); err == nil {
		t.Fatal("expected decryption with a different key to fail")
	}
}
