package adminplane

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A tokens file holds one accepted metrics token per line, by hash only:
//
//	sha256:<64 hex> name=<name> created=<YYYY-MM-DD>
//
// Blank lines and lines starting with # are ignored. The plaintext token is
// printed once, when created, and lives only with the scraper.

// TokenPrefix marks a metrics token, so a leaked one is recognizable.
const TokenPrefix = "ncli_mt_"

// minTokenBytes is the entropy of a generated token.
const minTokenBytes = 32

var tokenNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// TokenEntry is one line of a tokens file.
type TokenEntry struct {
	Hash    [sha256.Size]byte
	Name    string
	Created string
}

// LoadTokens reads and validates a tokens file.
func LoadTokens(path string) ([]TokenEntry, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, invalidErr(path, "admin.metrics.tokensFile: %v", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, &ConfigError{Err: fmt.Errorf("%s: %w", path, errTokensFileMode), Input: path}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, invalidErr(path, "admin.metrics.tokensFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	var out []TokenEntry
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		e, err := parseTokenLine(text)
		if err != nil {
			return nil, invalidErr(path, "%s:%d: %v", path, line, err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, invalidErr(path, "admin.metrics.tokensFile: %v", err)
	}
	return out, nil
}

func parseTokenLine(text string) (TokenEntry, error) {
	fields := strings.Fields(text)
	hashHex, ok := strings.CutPrefix(fields[0], "sha256:")
	if !ok {
		if strings.HasPrefix(fields[0], TokenPrefix) {
			return TokenEntry{}, errors.New("plaintext token found; store only its sha256 hash (use `ncli relay metrics token add`)")
		}
		return TokenEntry{}, errors.New("expected sha256:<hash>")
	}
	raw, err := hex.DecodeString(hashHex)
	if err != nil || len(raw) != sha256.Size {
		return TokenEntry{}, errors.New("hash must be 64 hex characters")
	}
	var e TokenEntry
	copy(e.Hash[:], raw)
	for _, f := range fields[1:] {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "name":
			e.Name = v
		case "created":
			e.Created = v
		}
	}
	return e, nil
}

// NewToken returns a random token and the tokens-file line for it.
func NewToken(name string, now time.Time) (token, line string, err error) {
	if !tokenNameRe.MatchString(name) {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidTokenName, name)
	}
	buf := make([]byte, minTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(token))
	line = fmt.Sprintf("sha256:%s name=%s created=%s", hex.EncodeToString(sum[:]), name, now.UTC().Format("2006-01-02"))
	return token, line, nil
}

// ErrInvalidTokenName is returned for a name that isn't 1-64 of
// [A-Za-z0-9._-].
var ErrInvalidTokenName = errors.New("token name must be 1-64 letters, digits, '.', '_' or '-'")

// ErrTokenNameTaken is returned by AddToken for a name already in the file.
var ErrTokenNameTaken = errors.New("a token with this name already exists")

// ErrTokenNotFound is returned by RemoveToken for an unknown name.
var ErrTokenNotFound = errors.New("no token with this name")

// AddToken generates a token named name, appends its hash to path (created
// 0600 if missing) and returns the plaintext token.
func AddToken(path, name string, now time.Time) (string, error) {
	if _, err := os.Stat(path); err == nil {
		existing, err := LoadTokens(path)
		if err != nil {
			return "", err
		}
		for _, e := range existing {
			if e.Name == name {
				return "", ErrTokenNameTaken
			}
		}
	}
	token, line, err := NewToken(name, now)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := fmt.Fprintln(f, line); err != nil {
		_ = f.Close()
		return "", err
	}
	return token, f.Close()
}

// RemoveToken deletes the line named name from path, keeping comments.
func RemoveToken(path, name string) error {
	if _, err := LoadTokens(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var kept []string
	found := false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			if e, err := parseTokenLine(trimmed); err == nil && e.Name == name {
				found = true
				continue
			}
		}
		kept = append(kept, line)
	}
	if !found {
		return ErrTokenNotFound
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// tokenSet checks bearer tokens against a tokens file, reloading it when
// its modification time changes, so tokens rotate without a restart.
type tokenSet struct {
	path string

	mu      sync.Mutex
	mtime   time.Time
	size    int64
	hashes  [][sha256.Size]byte
	checked time.Time
}

// recheckEvery bounds how often a request stats the tokens file.
const recheckEvery = time.Second

func newTokenSet(path string) (*tokenSet, error) {
	t := &tokenSet{path: path}
	if err := t.reload(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *tokenSet) reload() error {
	fi, err := os.Stat(t.path)
	if err != nil {
		return err
	}
	entries, err := LoadTokens(t.path)
	if err != nil {
		return err
	}
	hashes := make([][sha256.Size]byte, len(entries))
	for i, e := range entries {
		hashes[i] = e.Hash
	}
	t.hashes, t.mtime, t.size = hashes, fi.ModTime(), fi.Size()
	return nil
}

// valid reports whether token matches an entry. A file that turns invalid
// after startup (bad permissions, a plaintext line) rejects everything until
// fixed, rather than keeping stale tokens alive.
func (t *tokenSet) valid(token string, now time.Time) bool {
	if !strings.HasPrefix(token, TokenPrefix) {
		return false
	}
	sum := sha256.Sum256([]byte(token))

	t.mu.Lock()
	defer t.mu.Unlock()
	if now.Sub(t.checked) >= recheckEvery {
		t.checked = now
		if fi, err := os.Stat(t.path); err != nil || fi.Mode().Perm()&0o077 != 0 {
			// Forget mtime too, so a fix is picked up even if only the mode
			// changes back.
			t.hashes, t.mtime = nil, time.Time{}
		} else if !fi.ModTime().Equal(t.mtime) || fi.Size() != t.size {
			if err := t.reload(); err != nil {
				t.hashes = nil
			}
		}
	}
	match := 0
	for i := range t.hashes {
		match |= subtle.ConstantTimeCompare(sum[:], t.hashes[i][:])
	}
	return match == 1
}
