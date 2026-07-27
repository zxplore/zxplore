// favorites.go — saved locations: quick-connects (bookmarks) so you jump to a
// host/pool with two keys instead of retyping it. WinSCP "saved sessions" for
// ZFS. Persisted as JSON under the operator's config dir (stdlib only — no
// extra deps). Local or remote (SSH) entries; most-recent-first, capped.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Favorite is one saved location.
type Favorite struct {
	Name string `json:"name"`           // display label (defaults to the target)
	SSH  string `json:"ssh,omitempty"`  // "" = local; else "[user@]host"
	Path string `json:"path,omitempty"` // pool[/dataset] to land on
}

// Host is the connection this favorite points at.
func (f Favorite) Host() Host { return Host{SSH: f.SSH} }

// Target is the canonical "[host:]pool/path" string.
func (f Favorite) Target() string {
	if f.SSH == "" {
		return f.Path
	}
	return f.SSH + ":" + f.Path
}

func favoritesPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "zxplor", "favorites.json")
}

// LoadFavorites reads saved favorites (empty slice if none / unreadable).
func LoadFavorites() []Favorite {
	data, err := os.ReadFile(favoritesPath())
	if err != nil {
		return nil
	}
	var favs []Favorite
	if json.Unmarshal(data, &favs) != nil {
		return nil
	}
	return favs
}

// SaveFavorites writes favorites atomically (temp + rename), 0600.
func SaveFavorites(favs []Favorite) error {
	p := favoritesPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(favs, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// AddFavorite inserts f most-recent-first, de-duped by Name, capped at 50.
func AddFavorite(favs []Favorite, f Favorite) []Favorite {
	out := make([]Favorite, 0, len(favs)+1)
	out = append(out, f)
	for _, e := range favs {
		if e.Name != f.Name {
			out = append(out, e)
		}
	}
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

// ParseTarget turns "[user@]host:pool/path" or "pool/path" into a Favorite.
func ParseTarget(s string) Favorite {
	// Remote form has a ':' before any '/'.
	if i := indexColonBeforeSlash(s); i >= 0 {
		return Favorite{Name: s, SSH: s[:i], Path: s[i+1:]}
	}
	return Favorite{Name: s, Path: s}
}

func indexColonBeforeSlash(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ':':
			return i
		case '/':
			return -1
		}
	}
	return -1
}
