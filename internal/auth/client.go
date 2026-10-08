package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

const issuer = "https://auth.openai.com"
const resource = "https://api.openai.com/v1"

type Client struct{ http *http.Client }

func NewClient() *Client {
	dialer := net.Dialer{Timeout: 15 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 32 << 10}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" || host != "auth.openai.com" && host != "api.openai.com" {
			return nil, c.Fail(c.PolicyDenied, "auth destination outside official OpenAI origins")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			ip = ip.Unmap()
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, c.Fail(c.PolicyDenied, "auth destination has no allowed resolved address")
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (client *Client) Close() { client.http.CloseIdleConnections() }
func (client *Client) request(ctx context.Context, method, endpoint string, form url.Values, token string) ([]byte, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host != "auth.openai.com" && u.Host != "api.openai.com" {
		return nil, c.Fail(c.PolicyDenied, "invalid auth endpoint")
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		if strings.ContainsAny(token, "\r\n") {
			return nil, c.Fail(c.PolicyDenied, "invalid bearer handle")
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return nil, c.Fail(c.UnknownOutcome, "authentication transport interrupted; no automatic retry")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, c.Fail(c.UnknownOutcome, "authentication response incomplete or oversized")
	}
	if response.StatusCode != 200 {
		return nil, c.Fail(c.PolicyDenied, "OpenAI authentication or account request declined; reconnect or manage usage")
	}
	return raw, nil
}
func safeClientID(id string) bool {
	if id == "dynamic_agent_client" || len(id) < 8 || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

type identity struct{ Subject, Email string }

func (client *Client) verifyIdentity(ctx context.Context, token, clientID, nonce string) (identity, error) {
	var out identity
	parts := strings.Split(token, ".")
	if len(token) > 64<<10 || len(parts) != 3 || !safeClientID(clientID) {
		return out, c.Fail(c.PolicyDenied, "ID token unavailable or invalid")
	}
	decode := func(part string, target any) error {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(part)
		if err != nil {
			return err
		}
		var checked map[string]json.RawMessage
		if err = c.DecodeStrict(raw, &checked); err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		return decoder.Decode(target)
	}
	var header map[string]any
	if decode(parts[0], &header) != nil || header["alg"] != "RS256" || header["crit"] != nil || header["b64"] != nil {
		return out, c.Fail(c.PolicyDenied, "unsupported ID token signature profile")
	}
	kid, ok := header["kid"].(string)
	if !ok || kid == "" || len(kid) > 256 {
		return out, c.Fail(c.PolicyDenied, "ID token signing key unavailable")
	}
	jwksRaw, err := client.request(ctx, http.MethodGet, issuer+"/.well-known/jwks.json", nil, "")
	if err != nil {
		return out, err
	}
	var jwks map[string]json.RawMessage
	if err = c.DecodeStrict(jwksRaw, &jwks); err != nil {
		return out, c.Fail(c.PolicyDenied, "invalid OpenAI signing keys")
	}
	var keys []map[string]any
	if err = c.DecodeStrict(jwks["keys"], &keys); err != nil || len(keys) > 64 {
		return out, c.Fail(c.PolicyDenied, "invalid signing key quota")
	}
	var public *rsa.PublicKey
	for _, key := range keys {
		if key["kid"] != kid {
			continue
		}
		if public != nil || key["kty"] != "RSA" || key["use"] != nil && key["use"] != "sig" || key["alg"] != nil && key["alg"] != "RS256" {
			return out, c.Fail(c.PolicyDenied, "ID token signing key profile mismatch")
		}
		n, okN := key["n"].(string)
		e, okE := key["e"].(string)
		modulus, errN := base64.RawURLEncoding.Strict().DecodeString(n)
		exponent, errE := base64.RawURLEncoding.Strict().DecodeString(e)
		if !okN || !okE || errN != nil || errE != nil || len(modulus) < 256 || len(modulus) > 512 || len(exponent) < 1 || len(exponent) > 4 {
			return out, c.Fail(c.PolicyDenied, "invalid RSA signing key")
		}
		exp := new(big.Int).SetBytes(exponent).Int64()
		public = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(exp)}
		if public.N.BitLen() < 2048 || exp < 3 || exp > 2147483647 || exp%2 == 0 {
			return out, c.Fail(c.PolicyDenied, "weak signing key")
		}
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if public == nil || err != nil || rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signature) != nil {
		return out, c.Fail(c.PolicyDenied, "ID token signature rejected")
	}
	var claims map[string]any
	if decode(parts[1], &claims) != nil {
		return out, c.Fail(c.PolicyDenied, "invalid signed claims")
	}
	audience := false
	audienceCount := 0
	switch aud := claims["aud"].(type) {
	case string:
		audience = aud == clientID
		audienceCount = 1
	case []any:
		for _, a := range aud {
			if _, ok := a.(string); !ok {
				return out, c.Fail(c.PolicyDenied, "invalid ID token audience")
			}
			audience = audience || a == clientID
			audienceCount++
		}
	}
	seconds := func(key string) int64 {
		value, ok := claims[key].(json.Number)
		if !ok {
			return 0
		}
		n, _ := value.Int64()
		return n
	}
	if audienceCount > 1 && claims["azp"] != clientID || claims["azp"] != nil && claims["azp"] != clientID {
		return out, c.Fail(c.PolicyDenied, "ID token authorized party mismatch")
	}
	if claims["nbf"] != nil && seconds("nbf") < 1 || seconds("exp") <= seconds("iat") {
		return out, c.Fail(c.PolicyDenied, "invalid ID token time claims")
	}
	now := time.Now().Unix()
	subject, ok := claims["sub"].(string)
	claimNonce, _ := claims["nonce"].(string)
	if !ok || subject == "" || len(subject) > 512 || strings.ContainsAny(subject, "\r\n\x00") || claims["iss"] != issuer || !audience || seconds("exp") <= now-5 || seconds("iat") < 1 || seconds("iat") > now+5 || seconds("nbf") > now+5 || nonce != "" && subtle.ConstantTimeCompare([]byte(claimNonce), []byte(nonce)) != 1 {
		return out, c.Fail(c.PolicyDenied, "ID token issuer/audience/expiry/nonce/identity mismatch")
	}
	out.Subject = subject
	out.Email, _ = claims["email"].(string)
	if len(out.Email) > 512 || strings.ContainsAny(out.Email, "\r\n\x00") {
		return identity{}, c.Fail(c.PolicyDenied, "identity display quota exceeded")
	}
	return out, nil
}

type tokenResponse struct {
	ScopePresent bool
	AccessToken  string
	RefreshToken string
	IDToken      string
	Type         string
	Scope        string
	Expires      int64
}

func opaqueCredential(value string) bool {
	for _, b := range []byte(value) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return len(value) <= 64<<10
}
func parseTokens(raw []byte) (tokenResponse, error) {
	var values map[string]json.RawMessage
	var result tokenResponse
	if c.DecodeStrict(raw, &values) != nil {
		return result, c.Fail(c.PolicyDenied, "invalid OAuth response")
	}
	badType := false
	get := func(key string) string {
		raw, present := values[key]
		if !present {
			return ""
		}
		var value string
		if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
			badType = true
		}
		return value
	}
	result = tokenResponse{AccessToken: get("access_token"), RefreshToken: get("refresh_token"), IDToken: get("id_token"), Type: get("token_type"), Scope: get("scope")}
	_, result.ScopePresent = values["scope"]
	_ = json.Unmarshal(values["expires_in"], &result.Expires)
	if badType || result.AccessToken == "" || len(result.AccessToken) > 64<<10 || len(result.RefreshToken) > 64<<10 || len(result.IDToken) > 64<<10 || len(result.Scope) > 8192 || !strings.EqualFold(result.Type, "Bearer") || result.Expires < 1 || result.Expires > 86400 || !opaqueCredential(result.AccessToken) || !opaqueCredential(result.RefreshToken) {
		return tokenResponse{}, c.Fail(c.PolicyDenied, "invalid bounded OAuth token set")
	}
	return result, nil
}

type callback struct {
	Code, ClientID string
	Err            error
}

func (client *Client) Login(ctx context.Context, store *Store, profileID string, openBrowser func(string) error) error {
	clientID := "dynamic_agent_client"
	var prior *Profile
	if profileID != "" {
		var err error
		prior, err = store.profile(profileID)
		if err != nil {
			return err
		}
		clientID = prior.ClientID
	} else if store.data.PendingClient != "" {
		clientID = store.data.PendingClient
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	redirect := "http://" + listener.Addr().String() + "/auth/callback"
	stateValue, nonce, verifier := randomString(32), randomString(32), randomString(32)
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{"client_id": {clientID}, "ext_agent_host_id": {store.data.HostID}, "response_type": {"code"}, "redirect_uri": {redirect}, "scope": {"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"}, "resource": {resource}, "state": {stateValue}, "nonce": {nonce}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}}
	if clientID == "dynamic_agent_client" {
		query.Set("agent_name_hint", "Viber")
	}
	if prior != nil && prior.IDToken != "" {
		query.Set("id_token_hint", prior.IDToken)
	}
	if prior != nil && prior.Email != "" {
		query.Set("login_hint", prior.Email)
	}
	callbackCh := make(chan callback, 1)
	var consumed atomic.Bool
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method != http.MethodGet || r.URL.Path != "/auth/callback" || r.Host != listener.Addr().String() || len(r.URL.RawQuery) > 16384 {
			http.Error(w, "Invalid callback", 400)
			return
		}
		if consumed.Swap(true) {
			http.Error(w, "Callback already consumed", 409)
			return
		}
		q, parseErr := url.ParseQuery(r.URL.RawQuery)
		err := c.Fail(c.PolicyDenied, "sign-in callback rejected; retry login")
		valid := parseErr == nil && subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(stateValue)) == 1
		for _, key := range []string{"state", "code", "client_id", "error"} {
			if len(q[key]) > 1 {
				valid = false
			}
		}
		id := q.Get("client_id")
		if clientID != "dynamic_agent_client" {
			if id == "" {
				id = clientID
			}
			valid = valid && id == clientID
		}
		valid = valid && safeClientID(id) && q.Get("code") != "" && len(q.Get("code")) <= 8192 && q.Get("error") == ""
		if !valid {
			callbackCh <- callback{Err: err}
			http.Error(w, "Sign-in could not be verified", 400)
			return
		}
		callbackCh <- callback{Code: q.Get("code"), ClientID: id}
		_, _ = io.WriteString(w, "Sign-in callback received. Return to Viber.")
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	if err = openBrowser(issuer + "/api/accounts/authorize?" + query.Encode()); err != nil {
		return c.Fail(c.InvalidArgument, "could not open system browser for ChatGPT sign-in")
	}
	var result callback
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result = <-callbackCh:
	}
	if result.Err != nil {
		return result.Err
	}
	if prior == nil {
		store.data.PendingClient = result.ClientID
		if err = store.save(); err != nil {
			return err
		}
	}
	raw, err := client.request(ctx, http.MethodPost, issuer+"/api/accounts/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {result.ClientID}, "code": {result.Code}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {resource}}, "")
	if err != nil {
		return err
	}
	tokens, err := parseTokens(raw)
	if err != nil {
		return err
	}
	identity, err := client.verifyIdentity(ctx, tokens.IDToken, result.ClientID, nonce)
	if err != nil {
		return err
	}
	if prior != nil && identity.Subject != prior.Subject {
		return c.Fail(c.PolicyDenied, "selected account identity changed; credentials preserved")
	}
	profile := Profile{ClientID: result.ClientID, Issuer: issuer, Subject: identity.Subject, Email: identity.Email, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken, Scopes: strings.Fields(tokens.Scope), ExpiresAt: time.Now().Unix() + tokens.Expires}
	profile.ID, _ = c.Digest(struct{ Issuer, ClientID, Subject string }{profile.Issuer, profile.ClientID, profile.Subject})
	store.data.PendingClient = ""
	return store.put(profile)
}
func (client *Client) Access(ctx context.Context, store *Store, profileID string) (string, error) {
	p, err := store.profile(profileID)
	if err != nil {
		return "", err
	}
	if p.AccessToken != "" && p.ExpiresAt > time.Now().Unix()+60 {
		return store.activeToken(profileID)
	}
	if p.RefreshToken == "" {
		return "", c.Fail(c.PolicyDenied, "ChatGPT sign-in required")
	}
	raw, err := client.request(ctx, http.MethodPost, issuer+"/api/accounts/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {p.ClientID}, "refresh_token": {p.RefreshToken}, "resource": {resource}}, "")
	if err != nil {
		return "", err
	}
	tokens, err := parseTokens(raw)
	if err != nil {
		return "", err
	}
	updated := *p
	if tokens.IDToken != "" {
		identity, err := client.verifyIdentity(ctx, tokens.IDToken, p.ClientID, "")
		if err != nil {
			return "", err
		}
		if identity.Subject != p.Subject {
			return "", c.Fail(c.PolicyDenied, "refresh changed account identity")
		}
		updated.IDToken = tokens.IDToken
	}
	updated.AccessToken = tokens.AccessToken
	updated.ExpiresAt = time.Now().Unix() + tokens.Expires
	if tokens.RefreshToken != "" {
		updated.RefreshToken = tokens.RefreshToken
	}
	if tokens.ScopePresent {
		updated.Scopes = strings.Fields(tokens.Scope)
	}
	*p = updated
	if err = store.save(); err != nil {
		return "", err
	}
	return store.activeToken(profileID)
}
func (client *Client) Models(ctx context.Context, store *Store, profileID string) (json.RawMessage, error) {
	token, err := client.Access(ctx, store, profileID)
	if err != nil {
		return nil, err
	}
	raw, err := client.request(ctx, http.MethodGet, resource+"/models", nil, token)
	if err != nil {
		return nil, err
	}
	var catalog map[string]json.RawMessage
	if err = c.DecodeStrict(raw, &catalog); err != nil {
		return nil, c.Fail(c.PolicyDenied, "invalid account model catalog")
	}
	var entries []map[string]json.RawMessage
	if c.DecodeStrict(catalog["models"], &entries) != nil || len(entries) > 4096 {
		return nil, c.Fail(c.PolicyDenied, "invalid bounded account model catalog")
	}
	type choice struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
		Visibility  string `json:"visibility"`
	}
	models := []choice{}
	seen := map[string]bool{}
	for _, entry := range entries {
		var m choice
		_ = json.Unmarshal(entry["slug"], &m.Slug)
		_ = json.Unmarshal(entry["display_name"], &m.DisplayName)
		_ = json.Unmarshal(entry["visibility"], &m.Visibility)
		if m.Visibility != "list" {
			continue
		}
		if m.Slug == "" || len(m.Slug) > 256 || len(m.DisplayName) > 512 || strings.ContainsAny(m.Slug+m.DisplayName, "\r\n\x00") || seen[m.Slug] {
			return nil, c.Fail(c.PolicyDenied, "invalid displayed account model")
		}
		seen[m.Slug] = true
		models = append(models, m)
	}
	return json.Marshal(struct {
		Models []choice `json:"models"`
	}{models})
}
func (client *Client) Logout(ctx context.Context, store *Store, profileID string) (bool, error) {
	p, err := store.profile(profileID)
	if err != nil {
		return false, err
	}
	confirmed := p.RefreshToken == ""
	if p.RefreshToken != "" {
		raw, discoveryErr := client.request(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil, "")
		var discovery map[string]json.RawMessage
		if discoveryErr == nil && c.DecodeStrict(raw, &discovery) == nil {
			var endpoint string
			_ = json.Unmarshal(discovery["revocation_endpoint"], &endpoint)
			u, parseErr := url.Parse(endpoint)
			if parseErr == nil && u.Scheme == "https" && u.Host == "auth.openai.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
				_, revokeErr := client.request(ctx, http.MethodPost, endpoint, url.Values{"token": {p.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {p.ClientID}}, "")
				confirmed = revokeErr == nil
			}
		}
	}
	p.AccessToken = ""
	p.RefreshToken = ""
	p.IDToken = ""
	p.Scopes = nil
	p.ExpiresAt = 0
	return confirmed, store.save()
}

// AccessForModel checks the current selected account catalog before inference.
// It returns a process-local bearer value; callers must never persist it.
func (client *Client) AccessForModel(ctx context.Context, store *Store, profileID, modelID string) (string, error) {
	raw, err := client.Models(ctx, store, profileID)
	if err != nil {
		return "", err
	}
	var catalog map[string]json.RawMessage
	if c.DecodeStrict(raw, &catalog) != nil {
		return "", c.Fail(c.PolicyDenied, "invalid account model catalog")
	}
	var models []map[string]json.RawMessage
	if c.DecodeStrict(catalog["models"], &models) != nil || len(models) > 4096 {
		return "", c.Fail(c.PolicyDenied, "bounded account model catalog required")
	}
	for _, entry := range models {
		var slug, visibility string
		_ = json.Unmarshal(entry["slug"], &slug)
		_ = json.Unmarshal(entry["visibility"], &visibility)
		if slug == modelID && visibility == "list" {
			return store.activeToken(profileID)
		}
	}
	return "", c.Fail(c.PolicyDenied, "selected model unavailable to this ChatGPT account; use auth models")
}
