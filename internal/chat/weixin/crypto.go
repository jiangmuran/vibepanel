package weixin

import (
	"bytes"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// Media crypto: AES-128-ECB with PKCS#7, which is what the CDN speaks. ECB
// is a poor cipher and not our choice; the phone decrypts with the same
// mode, so anything else is an image that never opens.

func pkcs7Pad(b []byte) []byte {
	n := aes.BlockSize - len(b)%aes.BlockSize
	return append(append([]byte(nil), b...), bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 || len(b)%aes.BlockSize != 0 {
		return nil, errors.New("weixin: ciphertext is not a whole number of blocks")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		return nil, errors.New("weixin: bad padding")
	}
	for _, p := range b[len(b)-n:] {
		if int(p) != n {
			return nil, errors.New("weixin: bad padding")
		}
	}
	return b[:len(b)-n], nil
}

func encryptECB(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	src := pkcs7Pad(plain)
	out := make([]byte, len(src))
	for i := 0; i < len(src); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], src[i:i+aes.BlockSize])
	}
	return out, nil
}

func decryptECB(key, cipherText []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(cipherText)%aes.BlockSize != 0 {
		return nil, errors.New("weixin: ciphertext is not a whole number of blocks")
	}
	out := make([]byte, len(cipherText))
	for i := 0; i < len(cipherText); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], cipherText[i:i+aes.BlockSize])
	}
	return pkcs7Unpad(out)
}

// decodeMediaKey turns media.aes_key into 16 bytes. The field is base64 on
// the wire, but what is inside differs by media kind: 16 raw bytes for
// images, 32 hex characters for voice, file and video. Both are accepted
// whatever the item says it is, because the official client does the same
// and the server has not promised which one arrives.
func decodeMediaKey(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, fmt.Errorf("weixin: aes_key is not base64: %w", err)
	}
	switch len(raw) {
	case 16:
		return raw, nil
	case 32:
		key, err := hex.DecodeString(string(raw))
		if err != nil {
			return nil, fmt.Errorf("weixin: aes_key is 32 bytes but not hex: %w", err)
		}
		return key, nil
	}
	return nil, fmt.Errorf("weixin: aes_key decodes to %d bytes, want 16 or 32", len(raw))
}

// imageKey picks the key for an inbound image: the hex aeskey on the item
// first, then media.aes_key, then none, which means the bytes are plain.
func imageKey(it *imageItem) ([]byte, error) {
	if it.AESKey != "" {
		key, err := hex.DecodeString(it.AESKey)
		if err != nil || len(key) != 16 {
			return nil, fmt.Errorf("weixin: image aeskey is not 32 hex characters")
		}
		return key, nil
	}
	if it.Media != nil && it.Media.AESKey != "" {
		return decodeMediaKey(it.Media.AESKey)
	}
	return nil, nil
}

// base64Hex is the outbound aes_key encoding: base64 of the key's hex
// string, not of its bytes. That is what the official client sends and
// what the phone decodes; base64 of the raw bytes is an image that shows
// as broken.
func base64Hex(key []byte) string {
	return base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key)))
}
