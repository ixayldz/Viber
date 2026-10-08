package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(body []byte, status int) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
}
func jsonBytes(v any) []byte { raw, _ := json.Marshal(v); return raw }
func signed(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString(jsonBytes(map[string]any{"alg": "RS256", "kid": "fixture-key"}))
	payload := base64.RawURLEncoding.EncodeToString(jsonBytes(claims))
	digest := sha256.Sum256([]byte(header + "." + payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func jwks(key *rsa.PrivateKey) []byte {
	return jsonBytes(map[string]any{"keys": []any{map[string]any{"kid": "fixture-key", "kty": "RSA", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
}
func claims(nonce, subject string) map[string]any {
	return map[string]any{"iss": issuer, "aud": "viber-client-one", "sub": subject, "email": "same@example.test", "nonce": nonce, "iat": time.Now().Unix(), "exp": time.Now().Unix() + 3600}
}
func privateStore(t *testing.T) (*Store, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "credentials")
	s, err := Open(directory, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, directory
}

func TestIdentityRejectsSignatureNonceAudienceIssuerAndClockChanges(t *testing.T) {
	key := testKey(t)
	client := &Client{http: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != issuer+"/.well-known/jwks.json" || r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected key request")
		}
		return reply(jwks(key), 200), nil
	})}}
	base := claims("nonce", "subject")
	if id, err := client.verifyIdentity(context.Background(), signed(t, key, base), "viber-client-one", "nonce"); err != nil || id.Subject != "subject" {
		t.Fatalf("valid identity: %+v %v", id, err)
	}
	cases := map[string]func(map[string]any){
		"issuer":          func(m map[string]any) { m["iss"] = "https://attacker.test" },
		"audience":        func(m map[string]any) { m["aud"] = "other-client" },
		"multi audience":  func(m map[string]any) { m["aud"] = []string{"viber-client-one", "other-client"} },
		"nonce":           func(m map[string]any) { m["nonce"] = "wrong" },
		"missing nonce":   func(m map[string]any) { delete(m, "nonce") },
		"expired":         func(m map[string]any) { m["exp"] = time.Now().Unix() - 30 },
		"future":          func(m map[string]any) { m["iat"] = time.Now().Unix() + 30 },
		"nbf type":        func(m map[string]any) { m["nbf"] = "now" },
		"subject":         func(m map[string]any) { m["sub"] = "" },
		"display control": func(m map[string]any) { m["email"] = "x\nsecret" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m := claims("nonce", "subject")
			change(m)
			if _, err := client.verifyIdentity(context.Background(), signed(t, key, m), "viber-client-one", "nonce"); err == nil {
				t.Fatal("hostile identity admitted")
			}
		})
	}
	token := signed(t, key, base)
	parts := strings.Split(token, ".")
	parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 256))
	if _, err := client.verifyIdentity(context.Background(), strings.Join(parts, "."), "viber-client-one", "nonce"); err == nil {
		t.Fatal("bad signature admitted")
	}
	parts = strings.Split(token, ".")
	parts[0] = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"fixture-key"}`))
	if _, err := client.verifyIdentity(context.Background(), strings.Join(parts, "."), "viber-client-one", "nonce"); err == nil {
		t.Fatal("alg confusion admitted")
	}
}

func TestBrowserLoginPKCEIssuedRegistrationRefreshAndRedaction(t *testing.T) {
	s, directory := privateStore(t)
	key := testKey(t)
	var authorize url.Values
	tokenRequests := 0
	client := &Client{http: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			return reply(jwks(key), 200), nil
		case "/api/accounts/oauth/token":
			tokenRequests++
			raw, _ := io.ReadAll(r.Body)
			form, err := url.ParseQuery(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if form.Get("client_id") != "viber-client-one" || form.Get("resource") != resource || r.Header.Get("Authorization") != "" {
				t.Fatal("token exchange binding")
			}
			if tokenRequests == 1 {
				if s.data.PendingClient != "viber-client-one" {
					t.Fatal("issued registration not saved before exchange")
				}
				raw, err := os.ReadFile(filepath.Join(directory, "credentials.bin"))
				if err != nil {
					t.Fatal(err)
				}
				raw, err = unprotect(raw)
				if err != nil || !bytes.Contains(raw, []byte("viber-client-one")) {
					t.Fatal("registration not durable")
				}
				digest := sha256.Sum256([]byte(form.Get("code_verifier")))
				if base64.RawURLEncoding.EncodeToString(digest[:]) != authorize.Get("code_challenge") || form.Get("redirect_uri") != authorize.Get("redirect_uri") || form.Get("code") != "code-fixture" {
					t.Fatal("PKCE/redirect/code mismatch")
				}
				return reply(jsonBytes(map[string]any{"access_token": "access-fixture-one", "refresh_token": "refresh-fixture-one", "id_token": signed(t, key, claims(authorize.Get("nonce"), "subject-one")), "token_type": "Bearer", "expires_in": 3600, "scope": "openid resource.invoke chatgpt.tokens.use.direct"}), 200), nil
			}
			if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "refresh-fixture-one" || form.Has("scope") {
				t.Fatal("rotating refresh form")
			}
			return reply(jsonBytes(map[string]any{"access_token": "access-fixture-two", "refresh_token": "refresh-fixture-two", "token_type": "Bearer", "expires_in": 3600, "scope": "resource.invoke chatgpt.tokens.use.direct"}), 200), nil
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
			return nil, errors.New("unexpected")
		}
	})}}
	open := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		authorize = u.Query()
		if u.Host != "auth.openai.com" || u.Path != "/api/accounts/authorize" || authorize.Get("client_id") != "dynamic_agent_client" || authorize.Get("agent_name_hint") != "Viber" || authorize.Get("ext_agent_host_id") != s.data.HostID || authorize.Get("code_challenge_method") != "S256" {
			t.Fatal("authorize contract")
		}
		callback, err := url.Parse(authorize.Get("redirect_uri"))
		if err != nil {
			return err
		}
		if callback.Hostname() != "127.0.0.1" || callback.Path != "/auth/callback" {
			t.Fatal("callback not exact loopback")
		}
		callback.RawQuery = url.Values{"state": {authorize.Get("state")}, "code": {"code-fixture"}, "client_id": {"viber-client-one"}}.Encode()
		response, err := http.Get(callback.String())
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != 200 || bytes.Contains(body, []byte("code-fixture")) {
			t.Fatal("callback leaked code")
		}
		return nil
	}
	if err := client.Login(context.Background(), s, "", open); err != nil {
		t.Fatal(err)
	}
	view := s.View()
	if len(view.Profiles) != 1 || !view.Profiles[0].PlanUsage || !view.Profiles[0].Active {
		t.Fatalf("view %+v", view)
	}
	rendered := string(jsonBytes(view))
	for _, secret := range []string{"access-fixture", "refresh-fixture", "viber-client-one", "subject-one"} {
		if strings.Contains(rendered, secret) {
			t.Fatal("status leaked credential or private identity")
		}
	}
	p := &s.data.Profiles[0]
	p.ExpiresAt = time.Now().Unix() - 1
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	token, err := client.Access(context.Background(), s, p.ID)
	if err != nil || token != "access-fixture-two" || p.RefreshToken != "refresh-fixture-two" || tokenRequests != 2 {
		t.Fatalf("refresh: %s %v", token, err)
	}
	if _, err := Open(directory, false); err == nil {
		t.Fatal("concurrent credential owner admitted")
	}
	encrypted, err := os.ReadFile(filepath.Join(directory, "credentials.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && bytes.Contains(encrypted, []byte("access-fixture-two")) {
		t.Fatal("Windows credential not encrypted")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(directory, "credentials.bin"))
		if info.Mode().Perm() != 0600 {
			t.Fatal("credential mode")
		}
	}
}

func TestHostileCallbackAndReauthenticationPreserveCredentials(t *testing.T) {
	s, _ := privateStore(t)
	key := testKey(t)
	p := Profile{ClientID: "viber-client-one", Issuer: issuer, Subject: "first-subject", Email: "same@example.test", AccessToken: "keep-access", RefreshToken: "keep-refresh", IDToken: signed(t, key, claims("old", "first-subject")), Scopes: []string{"resource.invoke", "chatgpt.tokens.use.direct"}, ExpiresAt: time.Now().Unix() + 3600}
	p.ID, _ = c.Digest(struct{ Issuer, ClientID, Subject string }{p.Issuer, p.ClientID, p.Subject})
	if err := s.put(p); err != nil {
		t.Fatal(err)
	}
	for _, attack := range []string{"wrong-state", "wrong-client", "duplicate-state", "wrong-subject"} {
		t.Run(attack, func(t *testing.T) {
			count := 0
			var nonce string
			client := &Client{http: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/.well-known/jwks.json" {
					return reply(jwks(key), 200), nil
				}
				count++
				return reply(jsonBytes(map[string]any{"access_token": "attacker-access", "refresh_token": "attacker-refresh", "id_token": signed(t, key, claims(nonce, "second-subject")), "token_type": "Bearer", "expires_in": 3600, "scope": "resource.invoke chatgpt.tokens.use.direct"}), 200), nil
			})}}
			err := client.Login(context.Background(), s, p.ID, func(raw string) error {
				u, _ := url.Parse(raw)
				q := u.Query()
				nonce = q.Get("nonce")
				if q.Get("client_id") != p.ClientID || q.Get("id_token_hint") != p.IDToken {
					t.Fatal("saved registration not reused")
				}
				v := url.Values{"state": {q.Get("state")}, "client_id": {p.ClientID}, "code": {"private-code"}}
				switch attack {
				case "wrong-state":
					v.Set("state", "wrong")
				case "wrong-client":
					v.Set("client_id", "other-client-one")
				case "duplicate-state":
					v.Add("state", q.Get("state"))
				}
				cb, _ := url.Parse(q.Get("redirect_uri"))
				cb.RawQuery = v.Encode()
				resp, err := http.Get(cb.String())
				if err == nil {
					resp.Body.Close()
				}
				return err
			})
			if err == nil || strings.Contains(err.Error(), "attacker") || strings.Contains(err.Error(), "private-code") {
				t.Fatal("unsafe reauth/error")
			}
			if attack != "wrong-subject" && count != 0 {
				t.Fatal("untrusted callback exchanged")
			}
			actual, _ := s.profile(p.ID)
			if actual.AccessToken != p.AccessToken || actual.Subject != p.Subject {
				t.Fatal("wrong identity replaced selected account")
			}
		})
	}
}

func TestScopesRevokeFailureAndCorruptEnvelopeFailClosed(t *testing.T) {
	s, directory := privateStore(t)
	p := Profile{ClientID: "viber-client-one", Issuer: issuer, Subject: "subject", AccessToken: "access-private", RefreshToken: "refresh-private", Scopes: []string{"openid"}, ExpiresAt: time.Now().Unix() + 3600}
	p.ID, _ = c.Digest(struct{ Issuer, ClientID, Subject string }{p.Issuer, p.ClientID, p.Subject})
	if err := s.put(p); err != nil {
		t.Fatal(err)
	}
	count := 0
	client := &Client{http: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		count++
		return reply([]byte(`{"revocation_endpoint":"https://attacker.test/steal"}`), 200), nil
	})}}
	if _, err := client.Models(context.Background(), s, p.ID); err == nil || count != 0 {
		t.Fatal("identity scopes permitted plan inference")
	}
	confirmed, err := client.Logout(context.Background(), s, p.ID)
	if err != nil || confirmed || count != 1 {
		t.Fatalf("revocation result %v %v %d", confirmed, err, count)
	}
	actual, _ := s.profile(p.ID)
	if actual.AccessToken != "" || actual.RefreshToken != "" || actual.IDToken != "" || len(actual.Scopes) != 0 || s.View().Profiles[0].Connected {
		t.Fatal("logout retained local credential")
	}
	s.Close()
	if err := os.WriteFile(filepath.Join(directory, "credentials.bin"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(directory, false); err == nil {
		t.Fatal("empty/corrupt credential accepted")
	}
}

func TestIssuedRegistrationSurvivesLostExchange(t *testing.T) {
	s, _ := privateStore(t)
	client := &Client{http: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("private response body and secret")
	})}}
	for i := 0; i < 2; i++ {
		err := client.Login(context.Background(), s, "", func(raw string) error {
			u, _ := url.Parse(raw)
			q := u.Query()
			expected := "dynamic_agent_client"
			if i > 0 {
				expected = "viber-client-one"
			}
			if q.Get("client_id") != expected {
				t.Fatal("failed exchange lost issued registration")
			}
			cb, _ := url.Parse(q.Get("redirect_uri"))
			cb.RawQuery = url.Values{"state": {q.Get("state")}, "client_id": {"viber-client-one"}, "code": {"code-fixture"}}.Encode()
			response, err := http.Get(cb.String())
			if err == nil {
				response.Body.Close()
			}
			return err
		})
		if err == nil || strings.Contains(err.Error(), "private response") {
			t.Fatal("transport error disclosed underlying body")
		}
		if s.data.PendingClient != "viber-client-one" || len(s.View().Profiles) != 0 {
			t.Fatal("pending registration not preserved")
		}
	}
}

func TestCatalogIsAccountBoundOrderedRedactedAndLogoutRevokesRefresh(t *testing.T) {
	s, _ := privateStore(t)
	p := Profile{ClientID: "viber-client-one", Issuer: issuer, Subject: "subject", AccessToken: "access-private", RefreshToken: "refresh-private", Scopes: []string{"resource.invoke", "chatgpt.tokens.use.direct"}, ExpiresAt: time.Now().Unix() + 3600}
	p.ID, _ = c.Digest(struct{ Issuer, ClientID, Subject string }{p.Issuer, p.ClientID, p.Subject})
	if err := s.put(p); err != nil {
		t.Fatal(err)
	}
	client := &Client{http: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer access-private" {
				t.Fatal("catalog not selected account")
			}
			return reply([]byte(`{"models":[{"slug":"first","display_name":"First","visibility":"list","private":"access-private"},{"slug":"hidden","visibility":"hide"},{"slug":"second","display_name":"Second","visibility":"list"}],"extra":"refresh-private"}`), 200), nil
		case "/.well-known/openid-configuration":
			return reply([]byte(`{"revocation_endpoint":"https://auth.openai.com/api/accounts/oauth/revoke"}`), 200), nil
		case "/api/accounts/oauth/revoke":
			raw, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(raw))
			if form.Get("token") != "refresh-private" || form.Get("client_id") != "viber-client-one" || form.Get("token_type_hint") != "refresh_token" {
				t.Fatal("revocation binding")
			}
			return reply(nil, 200), nil
		}
		t.Fatal("unexpected endpoint")
		return nil, errors.New("unexpected")
	})}}
	raw, err := client.Models(context.Background(), s, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "hidden") || strings.Index(string(raw), "first") > strings.Index(string(raw), "second") {
		t.Fatal("catalog selection/redaction")
	}
	if token, err := client.AccessForModel(context.Background(), s, p.ID, "second"); err != nil || token != "access-private" {
		t.Fatal("selected model unavailable", err)
	}
	if _, err = client.AccessForModel(context.Background(), s, p.ID, "hidden"); err == nil {
		t.Fatal("hidden model admitted")
	}
	confirmed, err := client.Logout(context.Background(), s, p.ID)
	if err != nil || !confirmed || s.View().Profiles[0].Connected {
		t.Fatal("revocation not confirmed", err)
	}
}

func TestRefreshExplicitEmptyGrantedScopeCannotReuseOldPlanConsent(t *testing.T) {
	s, _ := privateStore(t)
	p := Profile{ClientID: "viber-client-one", Issuer: issuer, Subject: "subject", AccessToken: "expired", RefreshToken: "refresh", Scopes: []string{"resource.invoke", "chatgpt.tokens.use.direct"}, ExpiresAt: 1}
	p.ID, _ = c.Digest(struct{ Issuer, ClientID, Subject string }{p.Issuer, p.ClientID, p.Subject})
	if err := s.put(p); err != nil {
		t.Fatal(err)
	}
	client := &Client{http: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return reply([]byte(`{"access_token":"rotated","refresh_token":"rotated-refresh","token_type":"Bearer","expires_in":3600,"scope":""}`), 200), nil
	})}}
	if _, err := client.Access(context.Background(), s, p.ID); err == nil {
		t.Fatal("empty grant retained old plan consent")
	}
	if s.View().Profiles[0].PlanUsage {
		t.Fatal("revoked scope still shown enabled")
	}
}

func TestCredentialQuotaAndMalformedTokensCannotPoisonDurableFile(t *testing.T) {
	s, directory := privateStore(t)
	before, err := os.ReadFile(filepath.Join(directory, "credentials.bin"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		p := Profile{Issuer: issuer, ClientID: "viber-client-one", Subject: string(rune('a' + i)), AccessToken: strings.Repeat("a", 64<<10), RefreshToken: strings.Repeat("r", 64<<10), IDToken: strings.Repeat("i", 64<<10), ExpiresAt: time.Now().Unix() + 3600}
		p.ID, _ = c.Digest(struct{ Issuer, ClientID, Subject string }{p.Issuer, p.ClientID, p.Subject})
		s.data.Profiles = append(s.data.Profiles, p)
	}
	if err = s.save(); err == nil {
		t.Fatal("oversized credential state persisted")
	}
	after, err := os.ReadFile(filepath.Join(directory, "credentials.bin"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("quota refusal replaced durable file", err)
	}
	for _, raw := range []string{
		`{"access_token":"x\u0000y","token_type":"Bearer","expires_in":3600}`,
		`{"access_token":"x y","token_type":"Bearer","expires_in":3600}`,
		`{"access_token":"valid","refresh_token":123,"token_type":"Bearer","expires_in":3600}`,
		`{"access_token":"valid","scope":null,"token_type":"Bearer","expires_in":3600}`,
	} {
		if _, err := parseTokens([]byte(raw)); err == nil {
			t.Fatal("malformed token admitted")
		}
	}
}
