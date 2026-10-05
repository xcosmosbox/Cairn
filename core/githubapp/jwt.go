package githubapp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// signRS256 用 RSA private key 签发 RS256 JWT。
func signRS256(payload map[string]any, key *rsa.PrivateKey) (string, error) {
	if key == nil {
		return "", fmt.Errorf("githubapp: 签名 JWT: private key 不可为 nil")
	}
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	hb, _ := json.Marshal(header)
	pb, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("githubapp: 编码 JWT claims: %w", err)
	}
	enc := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	digest := sha256.Sum256([]byte(enc))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("githubapp: 签名 JWT: %w", err)
	}
	return enc + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
