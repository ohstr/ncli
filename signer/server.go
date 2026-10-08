package signer

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ohstr/ncli/client/nipcrypto"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/utils"
)

const (
	// MaxMessageSize bounds one request line.
	MaxMessageSize = 1 << 20
	// MethodStatus is ncli's own status method, outside NIP-46.
	MethodStatus = "signer_status"

	// Error prefixes a client can classify without string-matching the rest.
	ErrPrefixDenied  = "denied: "
	ErrPrefixInvalid = "invalid: "
)

// Config configures a Server.
type Config struct {
	PrivKeyHex string
	// GuardExtra are further encodings of the key to refuse in output
	// (the ncryptsec it was loaded from).
	GuardExtra []string

	PolicyPath string
	LoadOptions

	// StateDir persists consumed attestations. Required when the policy
	// uses attestations.
	StateDir string

	// AllowUIDs/AllowGIDs, when either is non-empty, restrict callers by
	// SO_PEERCRED.
	AllowUIDs []int
	AllowGIDs []int

	// Decisions receives every decision as NDJSON; Denials only denials.
	Decisions io.Writer
	Denials   io.Writer
	// Logf receives operational messages (reloads, errors).
	Logf func(format string, args ...any)

	Version string
	Now     func() time.Time
}

// Server answers NIP-46 method calls over a unix socket.
type Server struct {
	cfg     Config
	pub     string
	guard   *Guard
	policy  atomic.Pointer[Policy]
	loaded  atomic.Int64 // unix seconds of the last successful load
	started time.Time
	limiter *Limiter
	used    *UsedSet

	// signMu serializes evaluate -> consume -> sign so two requests can't
	// spend the same attestation.
	signMu sync.Mutex
	logMu  sync.Mutex

	allowUID map[int]bool
	allowGID map[int]bool
}

// New loads the policy and prepares a server. It does not listen.
func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Decisions == nil {
		cfg.Decisions = io.Discard
	}
	pub, err := utils.GetPublicKey(cfg.PrivKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}
	if (len(cfg.AllowUIDs) > 0 || len(cfg.AllowGIDs) > 0) && !PeerCredSupported {
		return nil, ErrPeerCredUnsupported
	}

	s := &Server{
		cfg:     cfg,
		pub:     pub,
		guard:   NewGuard(cfg.PrivKeyHex, cfg.GuardExtra...),
		started: cfg.Now(),
		limiter: NewLimiter(),
	}
	s.cfg.SignerPub = pub
	s.allowUID = intSet(cfg.AllowUIDs)
	s.allowGID = intSet(cfg.AllowGIDs)

	p, err := LoadPolicy(cfg.PolicyPath, s.cfg.LoadOptions)
	if err != nil {
		return nil, err
	}
	if cfg.StateDir != "" {
		if s.used, err = OpenUsedSet(cfg.StateDir, s.started); err != nil {
			return nil, fmt.Errorf("state dir: %w", err)
		}
	} else {
		s.used = NewMemoryUsedSet()
	}
	if err := s.checkPolicy(p); err != nil {
		_ = s.used.Close()
		return nil, err
	}
	s.policy.Store(p)
	s.loaded.Store(s.started.Unix())
	return s, nil
}

func intSet(xs []int) map[int]bool {
	if len(xs) == 0 {
		return nil
	}
	m := map[int]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func (s *Server) checkPolicy(p *Policy) error {
	if p.UsesAttestations() && !s.used.Persistent() {
		return errors.New("policy requires attestations: set --state-dir so a restart can't replay them")
	}
	return nil
}

// PubKey is the signer's public key (hex).
func (s *Server) PubKey() string { return s.pub }

// Policy is the active policy.
func (s *Server) Policy() *Policy { return s.policy.Load() }

// Reload re-reads the policy and its authors files. On error the active
// policy is kept.
func (s *Server) Reload() error {
	p, err := LoadPolicy(s.cfg.PolicyPath, s.cfg.LoadOptions)
	if err == nil {
		err = s.checkPolicy(p)
	}
	if err != nil {
		s.cfg.Logf("policy reload failed, keeping the active policy: %v", err)
		return err
	}
	s.policy.Store(p)
	s.loaded.Store(s.cfg.Now().Unix())
	s.cfg.Logf("policy reloaded (sha256 %s, %d rules)", p.SHA256, len(p.Rules))
	return nil
}

// Close releases the used-attestation file.
func (s *Server) Close() error {
	return s.used.Close()
}

// Serve accepts connections on l until ctx is done.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.serveConn(ctx, conn)
		}()
	}
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	peer := peerCredOf(conn)
	if !s.peerAllowed(peer) {
		s.record(Record{Method: "connect", Client: peer, Decision: "deny", Reason: "peer uid/gid not allowed"})
		return
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), MaxMessageSize)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req nip46.Request
		var resp nip46.Response
		if err := json.Unmarshal(line, &req); err != nil {
			resp.Error = ErrPrefixInvalid + "malformed request JSON"
		} else {
			resp = s.handle(req, peer)
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func (s *Server) peerAllowed(p Peer) bool {
	if s.allowUID == nil && s.allowGID == nil {
		return true
	}
	if p.UID != nil && s.allowUID[*p.UID] {
		return true
	}
	if p.GID != nil && s.allowGID[*p.GID] {
		return true
	}
	return false
}

// Status is signer_status's result.
type Status struct {
	Version      string `json:"version,omitempty"`
	PubKey       string `json:"pubkey"`
	PolicySHA256 string `json:"policy_sha256"`
	Rules        int    `json:"rules"`
	LoadedAt     int64  `json:"loaded_at"`
	UptimeS      int64  `json:"uptime_s"`
}

// handle answers one request. Exported behavior is per NIP-46 except that
// session methods are no-ops: the socket's permissions are the session.
func (s *Server) handle(req nip46.Request, peer Peer) nip46.Response {
	ok := func(result string) nip46.Response { return nip46.Response{RequestID: req.RequestID, Result: result} }
	fail := func(msg string) nip46.Response { return nip46.Response{RequestID: req.RequestID, Error: msg} }

	switch req.Method {
	case nip46.MethodPing:
		return ok("pong")
	case nip46.MethodGetPublicKey:
		return ok(s.pub)
	case nip46.MethodConnect, nip46.MethodLogout:
		return ok("ack")
	case nip46.MethodGetRelays:
		return ok("{}")
	case nip46.MethodSwitchRelays:
		return ok("null")
	case MethodStatus:
		p := s.policy.Load()
		b, _ := json.Marshal(Status{
			Version:      s.cfg.Version,
			PubKey:       s.pub,
			PolicySHA256: p.SHA256,
			Rules:        len(p.Rules),
			LoadedAt:     s.loaded.Load(),
			UptimeS:      int64(s.cfg.Now().Sub(s.started).Seconds()),
		})
		return ok(string(b))
	case nip46.MethodSignEvent:
		return s.handleSign(req, peer, ok, fail)
	}
	if nipcrypto.IsCryptoMethod(req.Method) {
		return s.handleCrypto(req, peer, ok, fail)
	}
	return fail(ErrPrefixInvalid + "unsupported method: " + req.Method)
}

func (s *Server) handleSign(req nip46.Request, peer Peer, ok, fail func(string) nip46.Response) nip46.Response {
	rec := Record{ReqID: req.RequestID, Method: req.Method, Client: peer}
	invalid := func(msg string) nip46.Response {
		rec.Decision, rec.Reason = "deny", msg
		s.record(rec)
		return fail(ErrPrefixInvalid + msg)
	}
	if len(req.Params) < 1 {
		return invalid("sign_event requires an event param")
	}
	var ev nip01.Event
	if err := json.Unmarshal([]byte(req.Params[0]), &ev); err != nil {
		return invalid("malformed event JSON")
	}
	rec.Kind = &ev.Kind
	var atts []*nip01.Event
	if len(req.Params) > 1 && strings.TrimSpace(req.Params[1]) != "" {
		if err := json.Unmarshal([]byte(req.Params[1]), &atts); err != nil {
			return invalid("attestations param must be a JSON array of events")
		}
	}

	denyWith := func(d Decision) nip46.Response {
		rec.Decision, rec.Rule, rec.Reason = "deny", d.Rule, d.Reason
		s.record(rec)
		return fail(ErrPrefixDenied + d.Reason)
	}

	if ev.PubKey != "" && !strings.EqualFold(ev.PubKey, s.pub) {
		return denyWith(Decision{Reason: "event pubkey does not match the signer"})
	}
	if err := PrepareTarget(&ev, s.pub); err != nil {
		return invalid(err.Error())
	}
	rec.EventID = ev.ID

	if s.guard.ContainsEvent(&ev) {
		return denyWith(Decision{Reason: "event contains the signer's key"})
	}

	s.signMu.Lock()
	defer s.signMu.Unlock()

	now := s.cfg.Now()
	d := Evaluate(Request{
		Method:       req.Method,
		Event:        &ev,
		Attestations: atts,
		Now:          now,
		StartTime:    s.started,
		SignerPub:    s.pub,
	}, s.policy.Load(), s.used.Has)
	if !d.Allow {
		return denyWith(d)
	}
	if d.Rate != nil && !s.limiter.Allow(d.Rule, *d.Rate, now) {
		return denyWith(Decision{Rule: d.Rule, Reason: "rate limit exceeded"})
	}

	var entries []UsedEntry
	for _, aid := range d.Consumed {
		for _, a := range atts {
			if a.ID == aid {
				entries = append(entries, UsedEntry{ID: aid, CreatedAt: int64(a.CreatedAt)})
				break
			}
		}
	}
	if err := s.used.Add(entries); err != nil {
		s.cfg.Logf("persisting used attestations failed: %v", err)
		return denyWith(Decision{Rule: d.Rule, Reason: "could not record attestation use"})
	}

	if err := ev.Sign(s.cfg.PrivKeyHex); err != nil {
		return fail("sign failed")
	}
	out, err := json.Marshal(&ev)
	if err != nil {
		return fail("encode failed")
	}
	rec.Decision, rec.Rule, rec.Attestations = "allow", d.Rule, d.Consumed
	s.record(rec)
	return ok(string(out))
}

func (s *Server) handleCrypto(req nip46.Request, peer Peer, ok, fail func(string) nip46.Response) nip46.Response {
	rec := Record{ReqID: req.RequestID, Method: req.Method, Client: peer}
	if len(req.Params) < 2 {
		rec.Decision, rec.Reason = "deny", "expected [pubkey, text] params"
		s.record(rec)
		return fail(ErrPrefixInvalid + rec.Reason)
	}
	counterpart, err := ParsePubkey(req.Params[0])
	if err != nil {
		rec.Decision, rec.Reason = "deny", "invalid counterpart pubkey"
		s.record(rec)
		return fail(ErrPrefixInvalid + rec.Reason)
	}
	rec.Counterpart = counterpart

	r := Request{Method: req.Method, Counterpart: counterpart, Now: s.cfg.Now(), StartTime: s.started, SignerPub: s.pub}
	encrypting := req.Method == nip46.MethodNIP04Encrypt || req.Method == nip46.MethodNIP44Encrypt
	if encrypting {
		r.Plaintext = req.Params[1]
		if s.guard.Contains(r.Plaintext) {
			rec.Decision, rec.Reason = "deny", "plaintext contains the signer's key"
			s.record(rec)
			return fail(ErrPrefixDenied + rec.Reason)
		}
	}
	d := Evaluate(r, s.policy.Load(), s.used.Has)
	if d.Allow && d.Rate != nil && !s.limiter.Allow(d.Rule, *d.Rate, r.Now) {
		d = Decision{Rule: d.Rule, Reason: "rate limit exceeded"}
	}
	rec.Rule = d.Rule
	if !d.Allow {
		rec.Decision, rec.Reason = "deny", d.Reason
		s.record(rec)
		return fail(ErrPrefixDenied + d.Reason)
	}
	result, err := nipcrypto.Do(req.Method, s.cfg.PrivKeyHex, []string{counterpart, req.Params[1]})
	if err != nil {
		rec.Decision, rec.Reason = "deny", "crypto failed"
		s.record(rec)
		return fail(req.Method + " failed: " + err.Error())
	}
	rec.Decision = "allow"
	s.record(rec)
	return ok(result)
}

// Record is one decision-log line. It never carries content or key
// material.
type Record struct {
	Time         string   `json:"time"`
	ReqID        string   `json:"req_id,omitempty"`
	Method       string   `json:"method"`
	Kind         *int     `json:"kind,omitempty"`
	EventID      string   `json:"event_id,omitempty"`
	Client       Peer     `json:"client"`
	Counterpart  string   `json:"counterpart,omitempty"`
	Decision     string   `json:"decision"`
	Rule         string   `json:"rule,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Attestations []string `json:"attestations,omitempty"`
}

func (s *Server) record(r Record) {
	r.Time = s.cfg.Now().UTC().Format(time.RFC3339)
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	line = append(line, '\n')
	s.logMu.Lock()
	defer s.logMu.Unlock()
	_, _ = s.cfg.Decisions.Write(line)
	if r.Decision == "deny" && s.cfg.Denials != nil {
		_, _ = s.cfg.Denials.Write(line)
	}
}

// ErrAlreadyListening means another signer holds the socket.
var ErrAlreadyListening = errors.New("a signer is already listening on this socket")

// PrepareTarget sets ev's pubkey to the signer's, clears any signature,
// and computes the id that attestations bind to.
func PrepareTarget(ev *nip01.Event, signerPub string) error {
	ev.PubKey, ev.ID, ev.Sig = strings.ToLower(signerPub), "", ""
	if ev.Tags == nil {
		ev.Tags = [][]string{}
	}
	id, err := ev.HashID()
	if err != nil {
		return fmt.Errorf("cannot hash event: %w", err)
	}
	ev.ID = hex.EncodeToString(id)
	return nil
}

// Listen binds a unix socket at path with mode, chowning it to uid/gid
// when either is >= 0. It refuses to replace a symlink or a live socket and
// removes a stale one. The parent directory is left alone: it is usually a
// volume shared with the clients.
func Listen(path string, mode os.FileMode, uid, gid int) (net.Listener, error) {
	if info, err := os.Lstat(path); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return nil, fmt.Errorf("refusing to bind over a symlink at %s", path)
		case info.Mode()&os.ModeSocket == 0:
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		case socketIsLive(path):
			return nil, fmt.Errorf("%w (%s)", ErrAlreadyListening, path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing stale socket %s: %w", path, err)
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(true)
	}
	if err := os.Chmod(path, mode); err != nil {
		_ = l.Close()
		return nil, err
	}
	if uid >= 0 || gid >= 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			_ = l.Close()
			return nil, err
		}
	}
	return l, nil
}

func socketIsLive(path string) bool {
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
