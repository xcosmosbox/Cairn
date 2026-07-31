// Package githubapp 提供 GitHub App 鉴权（JWT + installation token）。
// 从 dk-build/internal/controller/githubapp 提取到 core/ 共享，
// 使 dk-service（MCP Server）也能复用同一套鉴权。
package githubapp

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// AppAuth 管理 GitHub App 的 JWT 签发与 installation token 刷新。
// installation token 有 1 小时有效期，InstallToken 内部缓存并在过期前自动刷新。
type AppAuth struct {
	appID      int64
	privateKey *rsa.PrivateKey
	baseURL    string
	http       *http.Client

	mu         sync.Mutex
	instToken  string
	instExpiry time.Time
}

// NewAppAuth 从 PEM 编码的 private key 构造 AppAuth。
func NewAppAuth(appID int64, pemKey []byte, baseURL string) (*AppAuth, error) {
	if appID == 0 {
		return nil, fmt.Errorf("githubapp: app_id 不可为 0")
	}
	key, err := parsePrivateKey(pemKey)
	if err != nil {
		return nil, fmt.Errorf("githubapp: 解析 private key: %w", err)
	}
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &AppAuth{
		appID:      appID,
		privateKey: key,
		baseURL:    baseURL,
		http:       &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func parsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("PEM 解码失败（非有效 PEM）")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
	}
	return nil, fmt.Errorf("无法解析为 RSA private key")
}

// AppJWT 签发一个短期 JWT（GitHub App 认证用，RS256，有效期 10 分钟）。
func (a *AppAuth) AppJWT() (string, error) {
	now := time.Now()
	payload := map[string]any{
		"iat": now.Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": a.appID,
	}
	return signRS256(payload, a.privateKey)
}

// InstallToken 返回缓存的 installation token，过期前自动刷新。
func (a *AppAuth) InstallToken(ctx context.Context, installationID int64) (string, time.Time, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.instToken != "" && time.Until(a.instExpiry) > 60*time.Second {
		return a.instToken, a.instExpiry, nil
	}
	tok, exp, err := a.fetchInstallToken(ctx, installationID)
	if err != nil {
		return "", time.Time{}, err
	}
	a.instToken = tok
	a.instExpiry = exp
	return tok, exp, nil
}

// ResolveInstallation 按 owner/repo 查询 installation ID。
func (a *AppAuth) ResolveInstallation(ctx context.Context, owner, repo string) (int64, error) {
	jwt, err := a.AppJWT()
	if err != nil {
		return 0, err
	}
	u := a.baseURL + "/repos/" + owner + "/" + repo + "/installation"
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("githubapp: 查询 installation 失败 %d: %s", resp.StatusCode, string(body))
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&inst); err != nil {
		return 0, err
	}
	return inst.ID, nil
}

func (a *AppAuth) fetchInstallToken(ctx context.Context, installationID int64) (string, time.Time, error) {
	jwt, err := a.AppJWT()
	if err != nil {
		return "", time.Time{}, err
	}
	u := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.baseURL, installationID)
	req, _ := http.NewRequestWithContext(ctx, "POST", u, nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := a.http.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		return "", time.Time{}, fmt.Errorf("githubapp: 获取 installation token 失败 %d: %s", resp.StatusCode, string(body))
	}
	var tok struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", time.Time{}, err
	}
	exp, _ := time.Parse(time.RFC3339, tok.ExpiresAt)
	return tok.Token, exp, nil
}

// HTTPBaseURL 返回 API base URL。
func (a *AppAuth) HTTPBaseURL() string { return a.baseURL }
