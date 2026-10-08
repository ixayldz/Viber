// Package auth owns local ChatGPT plan credentials. Token material never enters
// task documents, source snapshots, journals, model context or status output.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

var validHostID = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Profile struct {
	ID           string   `json:"profile_id"`
	ClientID     string   `json:"client_id"`
	Issuer       string   `json:"issuer"`
	Subject      string   `json:"subject"`
	Email        string   `json:"email,omitempty"`
	Scopes       []string `json:"scopes"`
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	IDToken      string   `json:"id_token"`
	ExpiresAt    int64    `json:"expires_at"`
}
type state struct {
	PendingClient string    `json:"pending_registration,omitempty"`
	SchemaVersion int       `json:"schema_version"`
	HostID        string    `json:"host_id"`
	Active        string    `json:"active_profile"`
	Profiles      []Profile `json:"profiles"`
}
type ProfileView struct {
	ID        string `json:"profile_id"`
	Email     string `json:"email,omitempty"`
	Connected bool   `json:"connected"`
	PlanUsage bool   `json:"chatgpt_plan_usage"`
	Active    bool   `json:"active"`
}
type View struct {
	SchemaVersion        int           `json:"schema_version"`
	Provider             string        `json:"provider"`
	Profiles             []ProfileView `json:"profiles"`
	CredentialProtection string        `json:"credential_protection"`
	ManageUsage          string        `json:"manage_usage"`
}
type Store struct {
	root *os.Root
	lock *os.File
	data state
}

func randomString(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic("OS random source unavailable")
	}
	return hex.EncodeToString(raw)
}
func hostID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("OS random source unavailable")
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	h := hex.EncodeToString(b)
	return "urn:uuid:" + h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
func Open(directory string, create bool) (*Store, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) && create {
		err = os.MkdirAll(absolute, 0700)
		if err == nil {
			info, err = os.Lstat(absolute)
		}
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, c.Fail(c.PolicyDenied, "auth directory must be a private real directory")
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	if err = fileguard.Private(root); err != nil {
		root.Close()
		return nil, err
	}
	lock, err := fileguard.Lock(root, "auth.lock")
	if err != nil {
		root.Close()
		return nil, err
	}
	store := &Store{root: root, lock: lock}
	raw, err := fileguard.ReadRegular(root, "credentials.bin", 1<<20)
	if errors.Is(err, os.ErrNotExist) && create {
		store.data = state{SchemaVersion: 1, HostID: hostID(), Profiles: []Profile{}}
		err = store.save()
	} else if err == nil {
		raw, err = unprotect(raw)
		if err == nil {
			err = c.DecodeStrict(raw, &store.data)
		}
		if err == nil {
			err = store.validate()
		}
	}
	if err != nil {
		store.Close()
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, c.Fail(c.StoreIntegrityError, "auth storage unavailable or invalid")
	}
	return store, nil
}
func (s *Store) Close() error { return errors.Join(s.lock.Close(), s.root.Close()) }
func hasScope(p Profile, scope string) bool {
	for _, x := range p.Scopes {
		if x == scope {
			return true
		}
	}
	return false
}
func (s *Store) validate() error {
	if s.data.PendingClient != "" && !safeClientID(s.data.PendingClient) {
		return c.Fail(c.StoreIntegrityError, "invalid pending registration")
	}
	if s.data.SchemaVersion != 1 || !validHostID.MatchString(s.data.HostID) || len(s.data.Profiles) > 32 {
		return c.Fail(c.StoreIntegrityError, "invalid auth state")
	}
	seen := map[string]bool{}
	active := s.data.Active == ""
	for _, p := range s.data.Profiles {
		if !c.ValidDigest(p.ID) || seen[p.ID] || p.Issuer != "https://auth.openai.com" || !safeClientID(p.ClientID) || p.Subject == "" || len(p.Subject) > 512 || len(p.Email) > 512 || len(p.Scopes) > 32 || len(p.AccessToken) > 64<<10 || len(p.RefreshToken) > 64<<10 || len(p.IDToken) > 64<<10 || !opaqueCredential(p.AccessToken) || !opaqueCredential(p.RefreshToken) {
			return c.Fail(c.StoreIntegrityError, "invalid profile")
		}
		digest, _ := c.Digest(struct{ Issuer, ClientID, Subject string }{p.Issuer, p.ClientID, p.Subject})
		if digest != p.ID {
			return c.Fail(c.StoreIntegrityError, "profile identity mismatch")
		}
		if p.ExpiresAt < 0 || p.AccessToken != "" && p.ExpiresAt == 0 || strings.ContainsAny(p.Subject+p.Email+p.IDToken, "\r\n\x00") {
			return c.Fail(c.StoreIntegrityError, "invalid profile expiry or display")
		}
		scopeSeen := map[string]bool{}
		for _, scope := range p.Scopes {
			if scope == "" || len(scope) > 256 || scopeSeen[scope] || strings.ContainsAny(scope, " \t\r\n\x00") {
				return c.Fail(c.StoreIntegrityError, "invalid granted scopes")
			}
			scopeSeen[scope] = true
		}
		seen[p.ID] = true
		active = active || s.data.Active == p.ID
	}
	if !active {
		return c.Fail(c.StoreIntegrityError, "active account missing")
	}
	return nil
}
func (s *Store) save() error {
	if err := s.validate(); err != nil {
		return err
	}
	raw, err := c.CanonicalV1(s.data)
	if err != nil {
		return err
	}
	if len(raw) > (1<<20)-4096 {
		return c.Fail(c.BudgetLimitReached, "credential state quota exceeded")
	}
	raw, err = protect(raw)
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return c.Fail(c.BudgetLimitReached, "credential envelope quota exceeded")
	}
	// OS owner lock serializes rotating refreshes and atomic record replacement.
	temp := ".auth-" + randomString(16)
	f, err := s.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer s.root.Remove(temp)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = fileguard.RegularPath(s.root, "credentials.bin", true); err != nil {
		return err
	}
	if err = replaceCredential(s.root, temp, "credentials.bin"); err != nil {
		return err
	}
	return fileguard.SyncDirectory(s.root, ".")
}
func (s *Store) profile(id string) (*Profile, error) {
	if id == "" {
		id = s.data.Active
	}
	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			return &s.data.Profiles[i], nil
		}
	}
	return nil, c.Fail(c.PolicyDenied, "select a connected ChatGPT profile with auth login")
}
func (s *Store) View() View {
	view := View{SchemaVersion: 1, Provider: "chatgpt", Profiles: []ProfileView{}, CredentialProtection: credentialProtection, ManageUsage: "https://chatgpt.com/settings/usage"}
	for _, p := range s.data.Profiles {
		view.Profiles = append(view.Profiles, ProfileView{ID: p.ID, Email: p.Email, Connected: p.AccessToken != "", PlanUsage: p.AccessToken != "" && hasScope(p, "chatgpt.tokens.use.direct") && hasScope(p, "resource.invoke"), Active: s.data.Active == p.ID})
	}
	return view
}
func (s *Store) Select(id string) error {
	if _, err := s.profile(id); err != nil {
		return err
	}
	s.data.Active = id
	return s.save()
}
func (s *Store) put(profile Profile) error {
	for i, p := range s.data.Profiles {
		if p.ID == profile.ID {
			s.data.Profiles[i] = profile
			s.data.Active = profile.ID
			return s.save()
		}
	}
	if len(s.data.Profiles) >= 32 {
		return c.Fail(c.BudgetLimitReached, "account registration quota exceeded")
	}
	s.data.Profiles = append(s.data.Profiles, profile)
	s.data.Active = profile.ID
	return s.save()
}
func (s *Store) activeToken(profileID string) (string, error) {
	p, err := s.profile(profileID)
	if err != nil {
		return "", err
	}
	if p.AccessToken == "" || p.ExpiresAt <= time.Now().Unix()+30 || !hasScope(*p, "chatgpt.tokens.use.direct") || !hasScope(*p, "resource.invoke") {
		return "", c.Fail(c.PolicyDenied, "renew sign-in and grant ChatGPT plan usage before inference")
	}
	return p.AccessToken, nil
}
