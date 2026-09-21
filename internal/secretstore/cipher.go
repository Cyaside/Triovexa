package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	keySize      = 32
	cipherPrefix = "v1."
)

type Cipher struct {
	key [keySize]byte
}

// NewCipher loads an explicit base64 key or creates a local key file when the
// explicit value is empty. The key file is separate from encrypted database
// values so database backups never contain a usable provider credential.
func NewCipher(keyPath string, encodedKey string) (*Cipher, error) {
	encodedKey = strings.TrimSpace(encodedKey)
	var raw []byte
	var err error
	if encodedKey != "" {
		raw, err = base64.StdEncoding.DecodeString(encodedKey)
		if err != nil {
			return nil, fmt.Errorf("decode credential encryption key: %w", err)
		}
	} else {
		raw, err = loadOrCreateKey(strings.TrimSpace(keyPath))
		if err != nil {
			return nil, err
		}
	}
	if len(raw) != keySize {
		return nil, fmt.Errorf("credential encryption key must be %d bytes", keySize)
	}
	result := &Cipher{}
	copy(result.key[:], raw)
	return result, nil
}

func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if c == nil {
		return "", errors.New("credential cipher is unavailable")
	}
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return "", errors.New("credential is empty")
	}
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", fmt.Errorf("initialize credential cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("initialize credential encryption: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate credential nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append(nonce, sealed...)
	return cipherPrefix + base64.RawStdEncoding.EncodeToString(payload), nil
}

func (c *Cipher) Decrypt(encoded string) (string, error) {
	if c == nil {
		return "", errors.New("credential cipher is unavailable")
	}
	if !strings.HasPrefix(encoded, cipherPrefix) {
		return "", errors.New("unsupported credential ciphertext version")
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encoded, cipherPrefix))
	if err != nil {
		return "", fmt.Errorf("decode credential ciphertext: %w", err)
	}
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", fmt.Errorf("initialize credential cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("initialize credential decryption: %w", err)
	}
	if len(payload) <= gcm.NonceSize() {
		return "", errors.New("credential ciphertext is truncated")
	}
	plaintext, err := gcm.Open(nil, payload[:gcm.NonceSize()], payload[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("credential ciphertext could not be decrypted")
	}
	value := strings.TrimSpace(string(plaintext))
	if value == "" {
		return "", errors.New("decrypted credential is empty")
	}
	return value, nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("credential key path is empty")
	}
	content, err := os.ReadFile(path)
	if err == nil {
		return decodeKeyFile(content)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read credential key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create credential key directory: %w", err)
	}
	raw := make([]byte, keySize)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate credential key: %w", err)
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(raw) + "\n")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, fmt.Errorf("read concurrently created credential key: %w", readErr)
			}
			return decodeKeyFile(content)
		}
		return nil, fmt.Errorf("create credential key: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write credential key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close credential key: %w", err)
	}
	return raw, nil
}

func decodeKeyFile(content []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(content)))
	if err != nil {
		return nil, fmt.Errorf("decode credential key file: %w", err)
	}
	if len(raw) != keySize {
		return nil, fmt.Errorf("credential key file must contain %d bytes", keySize)
	}
	return raw, nil
}
