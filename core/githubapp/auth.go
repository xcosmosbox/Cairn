// Package githubapp 提供 GitHub App 鉴权（JWT + installation token）。
// 从 build/internal/controller/githubapp 提取到 core/ 共享，
// 使 service（MCP Server）也能复用同一套鉴权。
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
	"strings"
	"sync"
	"time"
	"unicode"
)

// AppAuth 管理 GitHub App 的 JWT 签发与 installation token 刷新。
// installation token 有 1 小时有效期，InstallToken 内部缓存并在过期前自动刷新。
type AppAuth struct {
	appID      int64
	privateKey *rsa.PrivateKey
	baseURL    string
	http       *http.Client
	// now 只在构造时设置；测试可注入线程安全时钟，避免等待真实到期。
	// Keep immutable after construction; tests may supply a concurrency-safe clock.
	now func() time.Time

	mu         sync.Mutex
	instTokens map[int64]installationToken
}

type installationToken struct {
	token  string
	expiry time.Time
}

// NewAppAuth 从 PEM 编码的 private key 构造 AppAuth。
func NewAppAuth(appID int64, pemKey []byte, baseURL string) (*AppAuth, error) {
	if appID <= 0 {
		return nil, fmt.Errorf("githubapp: app_id 必须为正数")
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
		now:        time.Now,
		instTokens: make(map[int64]installationToken),
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

// AppJWT 签发 RS256 JWT：iat 回拨 60 秒容忍时钟偏差，exp 至多为当前时间后 10 分钟。
// Backdate iat for clock drift; GitHub's ten-minute bound is relative to now,
// not exp-iat (which is eleven minutes with the recommended backdating).
func (a *AppAuth) AppJWT() (string, error) {
	now := a.now()
	payload := map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": a.appID,
	}
	return signRS256(payload, a.privateKey)
}

// InstallToken 返回缓存的 installation token，过期前自动刷新。
func (a *AppAuth) InstallToken(ctx context.Context, installationID int64) (string, time.Time, error) {
	if err := checkAuthContext(ctx); err != nil {
		return "", time.Time{}, err
	}
	if installationID <= 0 {
		return "", time.Time{}, fmt.Errorf("githubapp: installation_id 必须为正数")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// 等待另一个刷新期间可能已取消；缓存命中也不得绕过取消。
	// Recheck after waiting for a refresh, including the cache-hit path.
	if err := checkAuthContext(ctx); err != nil {
		return "", time.Time{}, err
	}
	if cached, ok := a.instTokens[installationID]; ok && cached.expiry.Sub(a.now()) > time.Minute {
		return cached.token, cached.expiry, nil
	}
	tok, exp, err := a.fetchInstallToken(ctx, installationID)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := checkAuthContext(ctx); err != nil {
		return "", time.Time{}, err
	}
	// 每个 installation 必须独立缓存；失败刷新不覆盖原来的有效记录。
	// Never return one installation's credentials for another installation.
	a.instTokens[installationID] = installationToken{token: tok, expiry: exp}
	return tok, exp, nil
}

// ResolveInstallation 按 owner/repo 查询 installation ID。
func (a *AppAuth) ResolveInstallation(ctx context.Context, owner, repo string) (int64, error) {
	if err := checkAuthContext(ctx); err != nil {
		return 0, err
	}
	jwt, err := a.AppJWT()
	if err != nil {
		return 0, err
	}
	u := a.baseURL + "/repos/" + owner + "/" + repo + "/installation"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, fmt.Errorf("githubapp: 构造 installation 查询请求: %w", err)
	}
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
	if inst.ID <= 0 {
		return 0, fmt.Errorf("githubapp: installation 响应缺少有效的正数 id")
	}
	if err := checkAuthContext(ctx); err != nil {
		return 0, err
	}
	return inst.ID, nil
}

func (a *AppAuth) fetchInstallToken(ctx context.Context, installationID int64) (string, time.Time, error) {
	if err := checkAuthContext(ctx); err != nil {
		return "", time.Time{}, err
	}
	jwt, err := a.AppJWT()
	if err != nil {
		return "", time.Time{}, err
	}
	u := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.baseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, "POST", u, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("githubapp: 构造 installation token 请求: %w", err)
	}
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
	// Token 是不透明凭证，不限制长度/前缀；但空值与头部非法字符绝不能当成功。
	// Tokens are opaque; reject empty/whitespace/control content, not new formats.
	if tok.Token == "" || strings.IndexFunc(tok.Token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", time.Time{}, fmt.Errorf("githubapp: installation token 响应包含无效 token")
	}
	exp, err := time.Parse(time.RFC3339, tok.ExpiresAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("githubapp: installation token expires_at 无效")
	}
	if !exp.After(a.now()) {
		return "", time.Time{}, fmt.Errorf("githubapp: installation token 已过期或到期时间不是将来")
	}
	if err := checkAuthContext(ctx); err != nil {
		return "", time.Time{}, err
	}
	return tok.Token, exp, nil
}

func checkAuthContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("githubapp: context 不可为 nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("githubapp: 鉴权请求已取消: %w", err)
	}
	return nil
}

// HTTPBaseURL 返回 API base URL。
func (a *AppAuth) HTTPBaseURL() string { return a.baseURL }
