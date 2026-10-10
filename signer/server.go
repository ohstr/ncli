package signer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ohstr/nmilat/nipLS"
)

// MethodStatus is ncli's own status method, outside NIP-46.
const MethodStatus = "signer_status"

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

// Server runs the YAML policy on top of a nipLS server.
type Server struct {
	cfg     Config
	ls      *nipLS.Server
	pub     string
	policy  atomic.Pointer[Policy]
	loaded  atomic.Int64 // unix seconds of the last successful load
	started time.Time
	limiter *Limiter
	used    *UsedSet

	// consumed carries attestation ids from authorize to the decision log.
	consumed sync.Map // *nipLS.Request -> []string
	logMu    sync.Mutex
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
	key, err := nipLS.NewLocalKey(cfg.PrivKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}
	if (len(cfg.AllowUIDs) > 0 || len(cfg.AllowGIDs) > 0) && !nipLS.PeerCredSupported {
		return nil, ErrPeerCredUnsupported
	}

	s := &Server{
		cfg:     cfg,
		pub:     key.PubKey(),
		started: cfg.Now(),
		limiter: NewLimiter(),
	}
	s.cfg.SignerPub = s.pub

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

	s.ls, err = nipLS.NewServer(nipLS.ServerConfig{
		Key:        key,
		Policy:     nipLS.PolicyFunc(s.authorize),
		Guard:      cfg.GuardExtra,
		AllowUIDs:  cfg.AllowUIDs,
		AllowGIDs:  cfg.AllowGIDs,
		Methods:    map[string]nipLS.Handler{MethodStatus: s.status},
		OnDecision: s.record,
		Now:        cfg.Now,
	})
	if err != nil {
		_ = s.used.Close()
		return nil, err
	}
	return s, nil
}

// ErrPeerCredUnsupported is returned when a caller allow-list is set on a
// platform without SO_PEERCRED.
var ErrPeerCredUnsupported = errors.New("--allow-uid/--allow-gid need SO_PEERCRED, which is only supported on Linux")

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
	return s.ls.Serve(ctx, l)
}

// authorize runs the YAML policy, the rate limit and attestation
// consumption. nipLS calls it for sign_event under its sign lock, so
// evaluate -> consume -> sign can't race.
func (s *Server) authorize(_ context.Context, req *nipLS.Request) error {
	now := s.cfg.Now()
	d := Evaluate(Request{
		Method:       req.Method,
		Event:        req.Event,
		Attestations: req.Attestations,
		Counterpart:  req.Counterpart,
		Plaintext:    req.Plaintext,
		Now:          now,
		StartTime:    s.started,
		SignerPub:    s.pub,
	}, s.policy.Load(), s.used.Has)
	req.Rule = d.Rule
	if !d.Allow {
		return nipLS.Deny(d.Reason)
	}
	if d.Rate != nil && !s.limiter.Allow(d.Rule, *d.Rate, now) {
		return nipLS.Deny("rate limit exceeded")
	}

	var entries []UsedEntry
	for _, aid := range d.Consumed {
		for _, a := range req.Attestations {
			if a.ID == aid {
				entries = append(entries, UsedEntry{ID: aid, CreatedAt: int64(a.CreatedAt)})
				break
			}
		}
	}
	if err := s.used.Add(entries); err != nil {
		s.cfg.Logf("persisting used attestations failed: %v", err)
		return nipLS.Deny("could not record attestation use")
	}
	if len(d.Consumed) > 0 {
		s.consumed.Store(req, d.Consumed)
	}
	return nil
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

func (s *Server) status(context.Context, *nipLS.Request) (string, error) {
	p := s.policy.Load()
	b, err := json.Marshal(Status{
		Version:      s.cfg.Version,
		PubKey:       s.pub,
		PolicySHA256: p.SHA256,
		Rules:        len(p.Rules),
		LoadedAt:     s.loaded.Load(),
		UptimeS:      int64(s.cfg.Now().Sub(s.started).Seconds()),
	})
	return string(b), err
}

// FetchStatus calls signer_status on c.
func FetchStatus(ctx context.Context, c *nipLS.Client) (*Status, error) {
	result, err := c.Call(ctx, MethodStatus)
	if err != nil {
		return nil, err
	}
	var st Status
	if err := json.Unmarshal([]byte(result), &st); err != nil {
		return nil, fmt.Errorf("malformed status: %w", err)
	}
	return &st, nil
}

// Peer identifies the calling process.
type Peer = nipLS.Peer

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

func (s *Server) record(d nipLS.Decision) {
	req := d.Request
	r := Record{
		Time:        d.Time.UTC().Format(time.RFC3339),
		ReqID:       req.ID,
		Method:      req.Method,
		Client:      req.Peer,
		Counterpart: req.Counterpart,
		Rule:        req.Rule,
		Decision:    "allow",
	}
	if r.Method == "" {
		r.Method = "connect"
	}
	if req.Event != nil {
		kind := req.Event.Kind
		r.Kind, r.EventID = &kind, req.Event.ID
	}
	if v, ok := s.consumed.LoadAndDelete(req); ok && d.Allowed() {
		r.Attestations = v.([]string)
	}
	if !d.Allowed() {
		r.Decision = "deny"
		var e *nipLS.Error
		if errors.As(d.Err, &e) {
			r.Reason = e.Reason
		} else {
			r.Reason = d.Err.Error()
		}
	}

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
