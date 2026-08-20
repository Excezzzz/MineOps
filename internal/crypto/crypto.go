// Package crypto шифрует куки Aternos (Fernet-совместимо с Python cryptography).
//
// Ключ берётся из ENCRYPTION_KEY в .env (если задан), иначе читается/создаётся
// файл data/secret.key — ровно как в Python-версии, поэтому существующие
// зашифрованные куки в БД расшифровываются без потери данных.
package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fernet/fernet-go"
)

var (
	keyFile = filepath.Join("data", "secret.key")
	keys    []*fernet.Key
)

// Init загружает (или генерирует) ключ шифрования.
func Init(encryptionKey string) error {
	envKey := encryptionKey
	if envKey != "" {
		if k, err := fernet.DecodeKey(envKey); err == nil {
			keys = []*fernet.Key{k}
			return nil
		}
	}

	if raw, err := os.ReadFile(keyFile); err == nil {
		k, err := fernet.DecodeKey(string(raw))
		if err != nil {
			return fmt.Errorf("secret.key повреждён: %w", err)
		}
		keys = []*fernet.Key{k}
		return nil
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	key := base64.URLEncoding.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, []byte(key), 0o600); err != nil {
		return err
	}
	k, err := fernet.DecodeKey(key)
	if err != nil {
		return err
	}
	keys = []*fernet.Key{k}
	return nil
}

// EncryptSession шифрует куку Aternos (пустая строка остаётся пустой).
func EncryptSession(plain string) string {
	if plain == "" {
		return ""
	}
	tok, err := fernet.EncryptAndSign([]byte(plain), keys[0])
	if err != nil {
		return ""
	}
	return string(tok)
}

// DecryptSession расшифровывает куку; повреждённые данные дают пустую строку.
func DecryptSession(encrypted string) string {
	if encrypted == "" {
		return ""
	}
	msg := fernet.VerifyAndDecrypt([]byte(encrypted), 0, keys)
	return string(msg)
}
