package ghapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeKey(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, key
}

func fakeGitHub(t *testing.T, key *rsa.PrivateKey, issued *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if jwt == "" || len(strings.Split(jwt, ".")) != 3 {
			http.Error(w, "no jwt", http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/repos/acme/api/installation", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			json.NewEncoder(w).Encode(map[string]any{"id": 777})
		}
	})
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			json.NewEncoder(w).Encode(map[string]any{"slug": "otto-review", "name": "Otto Review"})
		}
	})
	mux.HandleFunc("/app/installations", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 777, "repository_selection": "all", "account": map[string]any{"login": "acme", "type": "Organization"}},
				{"id": 888, "repository_selection": "selected", "account": map[string]any{"login": "victor", "type": "User"}},
			})
		}
	})
	mux.HandleFunc("/repos/acme/missing/installation", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/app/installations/777/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			*issued++
			json.NewEncoder(w).Encode(map[string]any{"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		}
	})
	return httptest.NewServer(mux)
}

func TestJWTIsSignedWithTheAppKeyAndCarriesTheAppID(t *testing.T) {
	path, key := writeKey(t)
	c := &Client{AppID: 12345, KeyPath: path, Now: func() time.Time { return time.Unix(1_800_000_000, 0) }}

	jwt, err := c.jwt()
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt = %q", jwt)
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	json.Unmarshal(raw, &claims)
	if claims.Iss != "12345" || claims.Exp-claims.Iat > 11*60 || claims.Iat >= 1_800_000_000 {
		t.Errorf("claims = %+v (iat must be backdated, lifetime under 10 minutes)", claims)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if err := verify(&key.PublicKey, parts[0]+"."+parts[1], sig); err != nil {
		t.Errorf("signature does not verify with the app key: %v", err)
	}
}

func TestTokenForRepoFindsInstallationAndCachesTheToken(t *testing.T) {
	path, key := writeKey(t)
	issued := 0
	srv := fakeGitHub(t, key, &issued)
	defer srv.Close()
	c := &Client{AppID: 1, KeyPath: path, BaseURL: srv.URL}

	for i := 0; i < 3; i++ {
		tok, err := c.TokenForRepo(context.Background(), "acme/api")
		if err != nil || tok != "ghs_installation_token" {
			t.Fatalf("token = %q, err = %v", tok, err)
		}
	}
	if issued != 1 {
		t.Errorf("issued %d installation tokens, want 1 (cached)", issued)
	}
}

func TestTokenForRepoExplainsWhenTheAppIsNotInstalled(t *testing.T) {
	path, key := writeKey(t)
	issued := 0
	srv := fakeGitHub(t, key, &issued)
	defer srv.Close()
	c := &Client{AppID: 1, KeyPath: path, BaseURL: srv.URL}

	_, err := c.TokenForRepo(context.Background(), "acme/missing")

	if err == nil || !strings.Contains(err.Error(), "not installed") || !strings.Contains(err.Error(), "acme/missing") {
		t.Errorf("err = %v, want a clear 'app is not installed on acme/missing'", err)
	}
	if err != nil && !strings.Contains(err.Error(), "https://github.com/apps/otto-review/installations/new") {
		t.Errorf("err = %v, want the link where the app is installed, so the fix is one click away", err)
	}
	if err != nil && !strings.Contains(err.Error(), "All repositories") {
		t.Errorf("err = %v, want the hint to pick All repositories", err)
	}
}

func TestMissingOrInvalidKeyFailsClearly(t *testing.T) {
	c := &Client{AppID: 1, KeyPath: filepath.Join(t.TempDir(), "nope.pem")}
	if _, err := c.TokenForRepo(context.Background(), "a/b"); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Errorf("err = %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.pem")
	os.WriteFile(bad, []byte("not a key"), 0o600)
	c = &Client{AppID: 1, KeyPath: bad}
	if _, err := c.TokenForRepo(context.Background(), "a/b"); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Errorf("err = %v", err)
	}
}

func TestValidateReturnsTheAppSlugSoSetupCanShowWhoWillPost(t *testing.T) {
	path, key := writeKey(t)
	issued := 0
	srv := fakeGitHub(t, key, &issued)
	defer srv.Close()
	c := &Client{AppID: 1, KeyPath: path, BaseURL: srv.URL}

	slug, err := c.Validate(context.Background())

	if err != nil || slug != "otto-review" {
		t.Errorf("slug = %q, err = %v", slug, err)
	}
}

func TestInstallURLPointsAtTheAppsInstallPage(t *testing.T) {
	if got := InstallURL("otto-connector"); got != "https://github.com/apps/otto-connector/installations/new" {
		t.Errorf("InstallURL = %q", got)
	}
}

func TestInstallationsListsWhereTheAppIsInstalledAndWhetherItSeesEveryRepository(t *testing.T) {
	path, key := writeKey(t)
	issued := 0
	srv := fakeGitHub(t, key, &issued)
	defer srv.Close()
	c := &Client{AppID: 1, KeyPath: path, BaseURL: srv.URL}

	got, err := c.Installations(context.Background())

	if err != nil {
		t.Fatal(err)
	}
	want := []Installation{{Account: "acme", AllRepositories: true}, {Account: "victor", AllRepositories: false}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("installations = %+v, want %+v", got, want)
	}
}
