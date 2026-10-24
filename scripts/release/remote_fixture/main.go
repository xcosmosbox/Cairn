// Command remote_fixture 为显式授权的私有沙箱排练提供打包和临时凭据。
// Credentials stay in a private runtime file; stdout contains only nonsecret metadata.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/xcosmosbox/cairn/core/githubapp"
	"github.com/xcosmosbox/cairn/core/kbbundle"
)

func main() {
	if len(os.Args) != 4 {
		panic("usage: remote_fixture pack BUNDLE ARCHIVE | token KEY PRIVATE_OUTPUT")
	}
	var err error
	if os.Args[1] == "pack" {
		err = kbbundle.PackTarball(os.Args[2], os.Args[3])
	} else if os.Args[1] == "token" {
		err = token(os.Args[2], os.Args[3])
	} else {
		err = fmt.Errorf("unknown mode")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func token(keyPath, dest string) error {
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("runtime key unavailable")
	}
	auth, err := githubapp.NewAppAuth(5454525, key, "https://api.github.com")
	if err != nil {
		return fmt.Errorf("native App auth construction failed")
	}
	jwt, err := auth.AppJWT()
	if err != nil {
		return fmt.Errorf("native JWT failed")
	}
	body := []byte(`{"repository_ids":[1432125826],"permissions":{"contents":"write","metadata":"read"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/app/installations/174795212/access_tokens", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Cairn-remote-recovery")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("token exchange transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return fmt.Errorf("token exchange HTTP %d", resp.StatusCode)
	}
	var data struct {
		Token       string            `json:"token"`
		ExpiresAt   string            `json:"expires_at"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&data); err != nil {
		return fmt.Errorf("token response decode failed")
	}
	if data.Token == "" || data.Permissions["contents"] != "write" {
		return fmt.Errorf("scoped token response invalid")
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("private token file create failed")
	}
	_, err = f.WriteString(data.Token)
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("private token write failed")
	}
	if closeErr != nil {
		return closeErr
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": resp.StatusCode, "expires_at": data.ExpiresAt, "permissions": data.Permissions, "repository_ids": []int64{1432125826}, "app_id": 5454525, "installation_id": 174795212})
}
