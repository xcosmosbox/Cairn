package githubapp

// 这里只验证真实签名运算与本地 HTTP 协议；临时 RSA key 不对应任何 GitHub App。
// Offline contract tests: generated RSA keys and localhost fixtures are not
// evidence that GitHub accepted a production private key or refreshed a live token.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var authTestKeyOnce sync.Once
var authTestKey *rsa.PrivateKey
var authTestKeyErr error

func temporaryRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	authTestKeyOnce.Do(func() { authTestKey, authTestKeyErr = rsa.GenerateKey(rand.Reader, 2048) })
	if authTestKeyErr != nil {
		t.Fatal(authTestKeyErr)
	}
	return authTestKey
}

type authClock struct{ seconds atomic.Int64 }

func newAuthClock() *authClock {
	c := &authClock{}
	c.seconds.Store(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Unix())
	return c
}
func (c *authClock) Now() time.Time          { return time.Unix(c.seconds.Load(), 0).UTC() }
func (c *authClock) Advance(d time.Duration) { c.seconds.Add(int64(d / time.Second)) }

func fixtureAuth(t *testing.T, base string, clock *authClock) *AppAuth {
	t.Helper()
	key := temporaryRSA(t)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	a, err := NewAppAuth(123, pemKey, base)
	if err != nil {
		t.Fatal(err)
	}
	a.now = clock.Now
	return a
}

type jwtClaims struct {
	Iat int64 `json:"iat"`
	Exp int64 `json:"exp"`
	Iss int64 `json:"iss"`
}

// 独立用 public key 验签，不能只解码待测实现自己生成的 claims 就算通过。
// Verify the signature with the public key, independently from signRS256.
func verifyFixtureJWT(token string, key *rsa.PublicKey, now time.Time) (jwtClaims, error) {
	var claims jwtClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims, fmt.Errorf("JWT must have three segments")
	}
	h, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims, err
	}
	p, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return claims, err
	}
	var header map[string]string
	if err := json.Unmarshal(h, &header); err != nil {
		return claims, err
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		return claims, fmt.Errorf("unexpected JWT header")
	}
	if err := json.Unmarshal(p, &claims); err != nil {
		return claims, err
	}
	if claims.Iss != 123 || claims.Iat > now.Unix() || claims.Exp <= now.Unix() || claims.Exp > now.Add(10*time.Minute).Unix() {
		return claims, fmt.Errorf("issuer or validity window rejected by local verifier")
	}
	return claims, nil
}

func requireJWTRequest(t *testing.T, r *http.Request, clock *authClock) bool {
	t.Helper()
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		t.Error("missing Bearer JWT")
		return false
	}
	if _, err := verifyFixtureJWT(strings.TrimPrefix(header, "Bearer "), &temporaryRSA(t).PublicKey, clock.Now()); err != nil {
		t.Errorf("local JWT verification failed: %v", err)
		return false
	}
	if r.Header.Get("Accept") != "application/vnd.github+json" {
		t.Error("missing GitHub media type")
		return false
	}
	return true
}

func writeFixtureToken(w http.ResponseWriter, token string, expiry time.Time) {
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token, "expires_at": expiry.Format(time.RFC3339)})
}

func TestAppJWTIndependentSignatureClaimsAndExpiry(t *testing.T) {
	t.Parallel()
	key := temporaryRSA(t)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, kind string
		der        []byte
	}{
		{"PKCS1", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)},
		{"PKCS8", "PRIVATE KEY", pkcs8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newAuthClock()
			a, err := NewAppAuth(123, pem.EncodeToMemory(&pem.Block{Type: tc.kind, Bytes: tc.der}), "")
			if err != nil {
				t.Fatal(err)
			}
			a.now = clock.Now
			jwt, err := a.AppJWT()
			if err != nil {
				t.Fatal(err)
			}
			claims, err := verifyFixtureJWT(jwt, &key.PublicKey, clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			if claims.Iat != clock.Now().Add(-time.Minute).Unix() || claims.Exp != clock.Now().Add(10*time.Minute).Unix() || claims.Exp-claims.Iat != 660 {
				t.Fatal("wrong skew allowance or expiry boundary")
			}
			parts := strings.Split(jwt, ".")
			parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"iss":456,"iat":0,"exp":9999999999}`))
			if _, err := verifyFixtureJWT(strings.Join(parts, "."), &key.PublicKey, clock.Now()); err == nil {
				t.Fatal("tampered payload accepted")
			}
			clock.Advance(10 * time.Minute)
			if _, err := verifyFixtureJWT(jwt, &key.PublicKey, clock.Now()); err == nil {
				t.Fatal("expired JWT accepted at exp boundary")
			}
			fresh, err := a.AppJWT()
			if err != nil {
				t.Fatal(err)
			}
			if fresh == jwt {
				t.Fatal("AppJWT reused expired claims")
			}
			if _, err := verifyFixtureJWT(fresh, &key.PublicKey, clock.Now()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthRejectsInvalidKeysIDsAndClaims(t *testing.T) {
	t.Parallel()
	validPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(temporaryRSA(t))})
	// 有效 key 使 appID 成为唯一失败因素，防止坏 key 掩盖负 ID 被接受。
	// A bad key would also fail on the old negative-ID path and mask the bug.
	for _, id := range []int64{0, -1} {
		if _, err := NewAppAuth(id, validPEM, ""); err == nil {
			t.Fatal("nonpositive app id accepted")
		}
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{nil, []byte("not PEM"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("bad DER")})} {
		if _, err := NewAppAuth(123, data, ""); err == nil {
			t.Fatal("invalid/non-RSA private key accepted")
		}
	}
	if jwt, err := signRS256(map[string]any{"invalid": make(chan int)}, temporaryRSA(t)); err == nil || jwt != "" {
		t.Fatal("unencodable JWT claims silently signed")
	}
	if jwt, err := signRS256(map[string]any{"iss": 123}, nil); err == nil || jwt != "" {
		t.Fatal("nil signing key accepted")
	}
}

func TestInstallTokenRefreshBoundaryAndInstallationIsolation(t *testing.T) {
	t.Parallel()
	clock := newAuthClock()
	var mu sync.Mutex
	calls := map[int]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireJWTRequest(t, r, clock) {
			http.Error(w, "bad local JWT", 401)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/installation" {
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 7})
			return
		}
		var id int
		if r.Method != http.MethodPost || (r.URL.Path != "/app/installations/7/access_tokens" && r.URL.Path != "/app/installations/8/access_tokens") {
			t.Errorf("unexpected auth request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 404)
			return
		}
		_, _ = fmt.Sscanf(r.URL.Path, "/app/installations/%d/access_tokens", &id)
		mu.Lock()
		calls[id]++
		count := calls[id]
		mu.Unlock()
		writeFixtureToken(w, fmt.Sprintf("fixture-%d-%d", id, count), clock.Now().Add(time.Hour))
	}))
	defer server.Close()
	a := fixtureAuth(t, server.URL, clock)
	if id, err := a.ResolveInstallation(context.Background(), "owner", "repo"); err != nil || id != 7 {
		t.Fatalf("resolve id=%d err=%v", id, err)
	}
	get := func(id int64, want string) {
		t.Helper()
		token, expiry, err := a.InstallToken(context.Background(), id)
		if err != nil || token != want || !expiry.After(clock.Now()) {
			t.Fatalf("installation %d wrong token/expiry, err=%v", id, err)
		}
	}
	get(7, "fixture-7-1")
	get(7, "fixture-7-1")
	clock.Advance(59*time.Minute - time.Second)
	get(7, "fixture-7-1")
	clock.Advance(time.Second)
	get(7, "fixture-7-2") // exactly 60 seconds remaining must refresh.
	get(8, "fixture-8-1")
	get(7, "fixture-7-2")
	mu.Lock()
	defer mu.Unlock()
	if calls[7] != 2 || calls[8] != 1 {
		t.Fatalf("wrong cache/refresh counts: %v", calls)
	}
}

func TestConcurrentInstallTokenRefreshOccursOnce(t *testing.T) {
	t.Parallel()
	clock := newAuthClock()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireJWTRequest(t, r, clock) {
			http.Error(w, "bad local JWT", 401)
			return
		}
		n := calls.Add(1)
		writeFixtureToken(w, fmt.Sprintf("fixture-%d", n), clock.Now().Add(time.Hour))
	}))
	defer server.Close()
	a := fixtureAuth(t, server.URL, clock)
	if _, _, err := a.InstallToken(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	clock.Advance(59 * time.Minute)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			token, _, err := a.InstallToken(context.Background(), 7)
			if err != nil || token != "fixture-2" {
				t.Errorf("concurrent refresh failed: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("refresh stampede: %d requests including initial mint", calls.Load())
	}
}

func TestFailedRefreshDoesNotPoisonCache(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"empty-token", "whitespace-token", "control-token", "missing-expiry", "bad-expiry", "expired", "expiry-now", "bad-json", "401", "403", "500"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			clock := newAuthClock()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !requireJWTRequest(t, r, clock) {
					http.Error(w, "bad local JWT", 401)
					return
				}
				n := calls.Add(1)
				if n != 2 {
					writeFixtureToken(w, fmt.Sprintf("fixture-%d", n), clock.Now().Add(time.Hour))
					return
				}
				for _, status := range []int{401, 403, 500} {
					if kind == fmt.Sprint(status) {
						http.Error(w, "synthetic upstream failure", status)
						return
					}
				}
				w.WriteHeader(http.StatusCreated)
				if kind == "bad-json" {
					_, _ = w.Write([]byte("{"))
					return
				}
				body := map[string]string{"token": "fixture-bad", "expires_at": clock.Now().Add(time.Hour).Format(time.RFC3339)}
				switch kind {
				case "empty-token":
					body["token"] = ""
				case "whitespace-token":
					body["token"] = "token with space"
				case "control-token":
					body["token"] = "token\x00value"
				case "missing-expiry":
					delete(body, "expires_at")
				case "bad-expiry":
					body["expires_at"] = "tomorrow"
				case "expired":
					body["expires_at"] = clock.Now().Add(-time.Second).Format(time.RFC3339)
				case "expiry-now":
					body["expires_at"] = clock.Now().Format(time.RFC3339)
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			a := fixtureAuth(t, server.URL, clock)
			first, expiry, err := a.InstallToken(context.Background(), 7)
			if err != nil {
				t.Fatal(err)
			}
			clock.Advance(59 * time.Minute)
			if token, exp, err := a.InstallToken(context.Background(), 7); err == nil || token != "" || !exp.IsZero() {
				t.Fatal("invalid token response returned success")
			}
			cached := a.instTokens[7]
			if cached.token != first || !cached.expiry.Equal(expiry) {
				t.Fatal("failed refresh overwrote cache")
			}
			if token, _, err := a.InstallToken(context.Background(), 7); err != nil || token != "fixture-3" {
				t.Fatalf("retry after failed refresh: %v", err)
			}
			if _, _, err := a.InstallToken(context.Background(), 7); err != nil || calls.Load() != 3 {
				t.Fatal("successful replacement was not cached")
			}
		})
	}
}

func TestOpaqueInstallationTokenHasNoFixedLengthAssumption(t *testing.T) {
	t.Parallel()
	clock := newAuthClock()
	longToken := "ghs_APPID_" + strings.Repeat("opaque-value.", 256)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeFixtureToken(w, longToken, clock.Now().Add(time.Hour))
	}))
	defer server.Close()
	if token, _, err := fixtureAuth(t, server.URL, clock).InstallToken(context.Background(), 7); err != nil || token != longToken {
		t.Fatalf("opaque token format rejected: %v", err)
	}
}

type authRoundTripper func(*http.Request) (*http.Response, error)

func (f authRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAuthCancellationInvalidIDsAndRequestConstruction(t *testing.T) {
	t.Parallel()
	clock := newAuthClock()
	a := fixtureAuth(t, "://malformed-url", clock)
	var calls atomic.Int32
	client := &http.Client{Transport: authRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body := fmt.Sprintf(`{"token":"fixture-valid","expires_at":%q}`, clock.Now().Add(time.Hour).Format(time.RFC3339))
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	a.http = client
	if _, err := a.ResolveInstallation(context.Background(), "owner", "repo"); err == nil {
		t.Fatal("malformed resolve URL accepted")
	}
	if _, _, err := a.InstallToken(context.Background(), 7); err == nil {
		t.Fatal("malformed token URL accepted")
	}
	// 用可成功的 URL/transport 隔离 ID 校验，避免 URL 错误掩盖发往无效 ID 的请求。
	// Valid transport makes removal of the ID guard observable as a request.
	a = fixtureAuth(t, "https://fixture.invalid", clock)
	a.http = client
	for _, id := range []int64{0, -7} {
		if _, _, err := a.InstallToken(context.Background(), id); err == nil {
			t.Fatal("nonpositive installation id accepted")
		}
	}
	a.instTokens[7] = installationToken{token: "cached", expiry: clock.Now().Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := a.InstallToken(ctx, 7); !errors.Is(err, context.Canceled) {
		t.Fatal("cache hit bypassed cancellation")
	}
	if _, err := a.ResolveInstallation(ctx, "owner", "repo"); !errors.Is(err, context.Canceled) {
		t.Fatal("resolve bypassed cancellation")
	}
	if _, _, err := a.InstallToken(nil, 7); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := a.ResolveInstallation(nil, "owner", "repo"); err == nil {
		t.Fatal("nil resolve context accepted")
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid request reached transport %d times", calls.Load())
	}
}

type observedAuthContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (c *observedAuthContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestCancellationWhileWaitingForRefreshDoesNotReturnCachedToken(t *testing.T) {
	t.Parallel()
	clock := newAuthClock()
	a := fixtureAuth(t, "https://fixture.invalid", clock)
	a.instTokens[7] = installationToken{token: "cached", expiry: clock.Now().Add(time.Hour)}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedAuthContext{Context: base, checked: make(chan struct{})}
	a.mu.Lock()
	done := make(chan error, 1)
	go func() {
		token, _, err := a.InstallToken(ctx, 7)
		if token != "" {
			done <- errors.New("canceled waiter received token")
			return
		}
		done <- err
	}()
	<-ctx.checked
	cancel()
	a.mu.Unlock()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter cancellation lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled waiter did not finish")
	}
}

func TestResolveInstallationRejectsInvalidResponses(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"id":0}`, `{"id":-1}`, `{"id":"7"}`, `{`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			if id, err := fixtureAuth(t, server.URL, newAuthClock()).ResolveInstallation(context.Background(), "owner", "repo"); err == nil || id != 0 {
				t.Fatal("malformed installation response accepted")
			}
		})
	}
}

func TestCancelInflightTokenExchangeDoesNotPopulateCache(t *testing.T) {
	t.Parallel()
	clock := newAuthClock()
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(stopped) }))
	defer server.Close()
	a := fixtureAuth(t, server.URL, clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := a.InstallToken(ctx, 7); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("token exchange did not begin")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight cancellation lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled exchange did not finish")
	}
	if len(a.instTokens) != 0 {
		t.Fatal("canceled exchange cached a token")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP request context was not canceled")
	}
}
