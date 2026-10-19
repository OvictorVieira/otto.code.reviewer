// Package ghapp lets Otto act as its own GitHub identity (otto[bot]) through a
// GitHub App: it signs a short-lived JWT with the app's private key and trades
// it for an installation token that is valid for one repository owner.
package ghapp

import (
	"crypto"
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
	"os"
	"strings"
	"sync"
	"time"

	"context"
)

// Client mints installation tokens for one GitHub App.
type Client struct {
	AppID   int64
	KeyPath string
	BaseURL string // default https://api.github.com
	Now     func() time.Time

	mu     sync.Mutex
	tokens map[int64]cached
}

type cached struct {
	token   string
	expires time.Time
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.github.com"
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (c *Client) privateKey() (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(c.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read the GitHub App private key %s: %w", c.KeyPath, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("the GitHub App private key %s is not a PEM file", c.KeyPath)
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the GitHub App private key %s is not a valid RSA key: %w", c.KeyPath, err)
	}
	k, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the GitHub App private key %s is not an RSA key", c.KeyPath)
	}
	return k, nil
}

// jwt builds the app-level token GitHub asks for. iat is backdated a minute
// to tolerate clock drift; the lifetime stays under GitHub's 10 minute limit.
func (c *Client) jwt() (string, error) {
	key, err := c.privateKey()
	if err != nil {
		return "", err
	}
	now := c.now()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": fmt.Sprint(c.AppID), "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(),
	})
	signing := b64(header) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

func verify(pub *rsa.PublicKey, signing string, sig []byte) error {
	sum := sha256.Sum256([]byte(signing))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig)
}

func (c *Client) call(ctx context.Context, method, path, jwt string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("github %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(body)))
	}
	return resp.StatusCode, json.Unmarshal(body, out)
}

// TokenForRepo returns an installation token for the account that owns repo.
// Tokens are cached until a minute before they expire.
func (c *Client) TokenForRepo(ctx context.Context, repo string) (string, error) {
	jwt, err := c.jwt()
	if err != nil {
		return "", err
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	if status, err := c.call(ctx, http.MethodGet, "/repos/"+repo+"/installation", jwt, &inst); err != nil {
		if status == http.StatusNotFound {
			return "", fmt.Errorf("the Otto GitHub App is not installed on %s: install it on that account or repository, then retry", repo)
		}
		return "", err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.tokens[inst.ID]; ok && c.now().Before(t.expires.Add(-time.Minute)) {
		return t.token, nil
	}
	var tok struct {
		Token   string    `json:"token"`
		Expires time.Time `json:"expires_at"`
	}
	if _, err := c.call(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID), jwt, &tok); err != nil {
		return "", err
	}
	if tok.Token == "" {
		return "", errors.New("github returned an empty installation token")
	}
	if c.tokens == nil {
		c.tokens = map[int64]cached{}
	}
	c.tokens[inst.ID] = cached{tok.Token, tok.Expires}
	return tok.Token, nil
}

// Validate checks the app id and key against GitHub and returns the app's
// slug, which is the name shown on reviews as <slug>[bot].
func (c *Client) Validate(ctx context.Context) (string, error) {
	jwt, err := c.jwt()
	if err != nil {
		return "", err
	}
	var app struct {
		Slug string `json:"slug"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/app", jwt, &app); err != nil {
		return "", fmt.Errorf("GitHub rejected this app id and key: %w", err)
	}
	return app.Slug, nil
}
