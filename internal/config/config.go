// Package config holds on-disk state: credentials, cached box connection and caches.
// Everything lives under $GB_HOME (default $XDG_CONFIG_HOME/gb or ~/.config/gb)/<profile>/, mode 0700/0600.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultBackend = "https://api2.cursor.sh"
	DefaultWebsite = "https://cursor.com"
	// Prod auth client id used by the Grok Bot desktop app (electron-main/main-core.cjs).
	DefaultClientID = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"
	// Client version reported to the backend. Old versions may get SAND_CLIENT_UPDATE_REQUIRED.
	DefaultClientVersion = "0.70.2"
)

type Credentials struct {
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	TeamID       string `json:"teamId,omitempty"`
	MachineID    string `json:"machineId,omitempty"`
}

// BoxConn is the gateway connection returned by EnsureSandBox.
type BoxConn struct {
	GatewayURL   string `json:"gatewayUrl"`
	GatewayToken string `json:"gatewayToken"`
	NetworkToken string `json:"networkToken,omitempty"`
	RunState     string `json:"runState,omitempty"`
	SavedAtMs    int64  `json:"savedAtMs"`
}

type Profile struct {
	Name string
	Dir  string
}

func Env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func Backend() string {
	return strings.TrimRight(Env("GB_BACKEND_URL", Env("CURSOR_API_BASE_URL", DefaultBackend)), "/")
}
func Website() string       { return strings.TrimRight(Env("GB_WEBSITE_URL", DefaultWebsite), "/") }
func ClientID() string      { return Env("SAND_AUTH_CLIENT_ID", DefaultClientID) }
func ClientVersion() string { return Env("SAND_CLIENT_VERSION", DefaultClientVersion) }
func Namespace() string     { return Env("SAND_BOX_NAMESPACE", "prod") }

func root() string {
	if v := os.Getenv("GB_HOME"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "gb")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gb")
}

// Root is the directory holding one sub-directory per account profile.
func Root() string { return root() }

// DefaultName is $GB_PROFILE, else the name stored by `gb accounts default`, else "default".
func DefaultName() string {
	if v := strings.TrimSpace(os.Getenv("GB_PROFILE")); v != "" {
		return v
	}
	if b, err := os.ReadFile(filepath.Join(root(), "default-profile")); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	return "default"
}

func SetDefault(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(root(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root(), "default-profile"), []byte(name+"\n"), 0o600)
}

func ValidName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\: `) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid account name %q (no spaces, ':' or '/')", name)
	}
	return nil
}

// Exists reports whether a profile with stored credentials exists.
func Exists(name string) bool {
	if ValidName(name) != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(root(), name, credFile))
	return err == nil
}

// List returns the names of all profiles that have credentials, sorted.
func List() []string {
	ents, _ := os.ReadDir(root())
	var out []string
	for _, e := range ents {
		if e.IsDir() && Exists(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Delete removes a profile directory entirely.
func Delete(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(root(), name))
}

func Open(name string) (*Profile, error) {
	if name == "" {
		name = DefaultName()
	}
	if err := ValidName(name); err != nil {
		return nil, err
	}
	dir := filepath.Join(root(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Profile{Name: name, Dir: dir}, nil
}

func (p *Profile) path(f string) string { return filepath.Join(p.Dir, f) }

// ReadJSON returns os.ErrNotExist when the file is missing.
func (p *Profile) ReadJSON(file string, v any) error {
	b, err := os.ReadFile(p.path(file))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// WriteJSON writes atomically with mode 0600.
func (p *Profile) WriteJSON(file string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(p.Dir, "."+file+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p.path(file))
}

func (p *Profile) Remove(file string) error {
	err := os.Remove(p.path(file))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

const (
	credFile    = "credentials.json"
	boxFile     = "box.json"
	AgentsCache = "agents-cache.json"
)

// Credentials loads stored credentials, honoring env overrides (CURSOR_ACCESS_TOKEN / GB_REFRESH_TOKEN).
// A machine id is created and persisted on first use.
func (p *Profile) Credentials() (*Credentials, error) {
	c := &Credentials{}
	if err := p.ReadJSON(credFile, c); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if c.MachineID == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		c.MachineID = hex.EncodeToString(b)
		if err := p.WriteJSON(credFile, c); err != nil {
			return nil, err
		}
	}
	if v := os.Getenv("CURSOR_ACCESS_TOKEN"); v != "" {
		c.AccessToken = strings.TrimSpace(v)
	}
	if v := os.Getenv("GB_REFRESH_TOKEN"); v != "" {
		c.RefreshToken = strings.TrimSpace(v)
	}
	return c, nil
}

func (p *Profile) SaveCredentials(c *Credentials) error { return p.WriteJSON(credFile, c) }

func (p *Profile) Box() (*BoxConn, error) {
	b := &BoxConn{}
	if err := p.ReadJSON(boxFile, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (p *Profile) SaveBox(b *BoxConn) error { return p.WriteJSON(boxFile, b) }
func (p *Profile) ClearBox() error          { return p.Remove(boxFile) }

// Logout removes everything except the machine id.
func (p *Profile) ClearAuth() error {
	c, _ := p.Credentials()
	keep := &Credentials{}
	if c != nil {
		keep.MachineID = c.MachineID
	}
	_ = p.Remove(boxFile)
	_ = p.Remove(AgentsCache)
	return p.SaveCredentials(keep)
}
