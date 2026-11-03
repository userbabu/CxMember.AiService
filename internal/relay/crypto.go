package relay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

func decryptCredential(key string, ciphertext []byte) (string, error) {
	if key == "" {
		return "", errors.New("ENCRYPTION_KEY is required")
	}
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(key)
	}
	if err != nil {
		raw = []byte(key)
	}
	derived := sha256.Sum256(raw)
	block, err := aes.NewCipher(derived[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted provider credential")
	}
	plain, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
