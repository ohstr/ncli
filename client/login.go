package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip42"
	"github.com/ohstr/nmilat/nip46"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/wire"
	"github.com/rs/zerolog/log"
)

// NIP-42 refusal prefixes: "auth-required:" before login, "restricted:"
// once logged in but still not allowed.
const (
	authRequiredPrefix = "auth-required:"
	restrictedPrefix   = "restricted:"
)

// loginWait bounds waiting for a login to resolve. It covers a remote
// signer's round trip, so it's longer than a relay's.
var loginWait = 35 * time.Second

// challengeWait is how long loginFirst waits for a challenge on connect.
var challengeWait = 2 * time.Second

// LoginReason says why a NIP-42 login failed.
type LoginReason string

const (
	// LoginRequired: the relay wants a login and no signer was given.
	LoginRequired LoginReason = "login_required"
	// LoginSignerRefused: the signer refused to sign the login event.
	LoginSignerRefused LoginReason = "signer_refused"
	// LoginRelayRefused: the relay refused the login, or the logged-in identity.
	LoginRelayRefused LoginReason = "relay_refused"
)

// LoginError is a failed NIP-42 login on one relay. Detail is the
// relay's refusal message or the signer's reason. errors.Is matches
// ErrRestricted, so callers that map a refused query keep working.
type LoginError struct {
	Relay  string
	Reason LoginReason
	Detail string
}

func (e *LoginError) Error() string {
	switch e.Reason {
	case LoginRequired:
		return fmt.Sprintf("%s requires login (NIP-42) and no signer was given: %s", e.Relay, e.Detail)
	case LoginSignerRefused:
		return fmt.Sprintf("signer refused to log in to %s: %s", e.Relay, e.Detail)
	default:
		return fmt.Sprintf("%s refused login: %s", e.Relay, e.Detail)
	}
}

func (e *LoginError) Is(target error) bool { return target == ErrRestricted }

// relaySession is one dialed connection and its NIP-42 login. Whoever
// reads conn.Read() passes each message through observe, which answers
// the relay's first AUTH challenge (one login per connection, never
// more) and records the relay's OK for it.
type relaySession struct {
	conn   *relayclient.Connection
	relay  string // the URL as dialed; the only relay a login may name
	signer Signer

	mu       sync.Mutex
	asked    bool
	loginID  string
	state    relayclient.AuthState
	failure  error
	askedCh  chan struct{} // closed on the first challenge
	settleCh chan struct{} // closed once the login succeeded or failed
}

// dialRelay dials primary, then fallback (may be nil) if primary fails to
// connect -- see connectRelayWithFallback. signer nil stays anonymous.
func dialRelay(ctx context.Context, primary, fallback *url.URL, cfg *relayclient.ConnectionConfig, signer Signer) (*relaySession, error) {
	s, err := dialRelayOnce(ctx, primary, cfg, signer)
	if err == nil || fallback == nil {
		return s, err
	}
	var connErr *relayclient.ConnectionError
	if !errors.As(err, &connErr) {
		return nil, err
	}
	return dialRelayOnce(ctx, fallback, cfg, signer)
}

func dialRelayOnce(ctx context.Context, u *url.URL, cfg *relayclient.ConnectionConfig, signer Signer) (*relaySession, error) {
	c := relayclient.DefaultConnectionConfig()
	if cfg != nil {
		cc := *cfg
		c = &cc
	}
	// The session answers AUTH itself; the connection must not.
	c.SigningKeyHex = ""
	c.Signer = nil
	conn, err := relayclient.NewConnection(ctx, u, c)
	if err != nil {
		return nil, err
	}
	return &relaySession{
		conn:     conn,
		relay:    u.String(),
		signer:   signer,
		askedCh:  make(chan struct{}),
		settleCh: make(chan struct{}),
	}, nil
}

func (s *relaySession) Close() { s.conn.Close() }

// observe handles login traffic: an AUTH challenge or the OK for this
// session's login. It reports whether msg was one, so the caller skips it.
func (s *relaySession) observe(msg wire.SubscriptionResponse) bool {
	switch m := msg.(type) {
	case *wire.AuthChallengeResponse:
		if s.signer == nil {
			return true
		}
		s.mu.Lock()
		first := !s.asked
		s.asked = true
		s.mu.Unlock()
		if first {
			close(s.askedCh)
			go s.sendLogin(m.Challenge)
		}
		return true
	case *wire.OkSubscriptionResponse:
		s.mu.Lock()
		ours := s.loginID != "" && m.EventID == s.loginID
		s.mu.Unlock()
		if !ours {
			return false
		}
		if m.Accepted {
			s.settle(relayclient.AuthStateSucceeded, nil)
		} else {
			s.settle(relayclient.AuthStateFailed, &LoginError{Relay: s.relay, Reason: LoginRelayRefused, Detail: m.Message})
		}
		return true
	}
	return false
}

// sendLogin signs a kind 22242 for this connection's relay and challenge
// and sends it. The signed event is never logged.
func (s *relaySession) sendLogin(challenge string) {
	ctx, cancel := context.WithTimeout(context.Background(), loginWait)
	defer cancel()
	go func() {
		select {
		case <-s.conn.Closed():
			cancel()
		case <-ctx.Done():
		}
	}()

	ev, err := signLogin(ctx, s.signer, s.relay, challenge)
	if err != nil {
		var refusal *nip46.Error
		var le *LoginError
		switch {
		case errors.As(err, &refusal):
			err = &LoginError{Relay: s.relay, Reason: LoginSignerRefused, Detail: refusal.Wire()}
		case errors.As(err, &le):
			le.Relay = s.relay
		default:
			err = fmt.Errorf("signing the login for %s: %w", s.relay, err)
		}
		s.settle(relayclient.AuthStateFailed, err)
		return
	}
	s.mu.Lock()
	s.loginID = ev.ID
	s.mu.Unlock()
	log.Debug().Str("relay", s.relay).Msg("logging in (NIP-42)")
	select {
	case s.conn.Outgoing() <- &wire.AuthPacket{Event: ev}:
	case <-s.conn.Closed():
	}
}

// signLogin has signer sign a kind 22242 for relay and challenge, with
// created_at now and empty content, and checks it got back that event.
func signLogin(ctx context.Context, signer Signer, relay, challenge string) (*nip01.Event, error) {
	ev := nip42.NewAuthEvent(challenge, relay)
	want := *ev
	if err := signer.Sign(ctx, ev); err != nil {
		return nil, err
	}
	if ev.Kind != nip42.KindClientAuth || ev.Content != "" || !reflect.DeepEqual(ev.Tags, want.Tags) || ev.Verify() != nil {
		return nil, &LoginError{Reason: LoginSignerRefused, Detail: "signer returned a different login event"}
	}
	return ev, nil
}

func (s *relaySession) settle(state relayclient.AuthState, failure error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != relayclient.AuthStateNone {
		return
	}
	s.state = state
	s.failure = failure
	close(s.settleCh)
}

// loggedIn reports whether this connection's login succeeded.
func (s *relaySession) loggedIn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == relayclient.AuthStateSucceeded
}

// challenged reports whether this connection answered a challenge.
func (s *relaySession) challenged() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked && s.signer != nil
}

func (s *relaySession) result() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

// refusedForLogin reports whether msg asks the client to log in.
func refusedForLogin(msg string) bool { return strings.HasPrefix(msg, authRequiredPrefix) }

// awaitLogin waits for this connection's login to resolve after the relay
// refused something with refusal. It keeps reading the connection, passing
// every other message to other (nil drops them), and returns nil once
// logged in or why not.
func (s *relaySession) awaitLogin(ctx context.Context, refusal string, other func(wire.SubscriptionResponse)) error {
	if s.signer == nil {
		return &LoginError{Relay: s.relay, Reason: LoginRequired, Detail: refusal}
	}
	timeout := time.NewTimer(loginWait)
	defer timeout.Stop()
	for {
		select {
		case <-s.settleCh:
			return s.result()
		default:
		}
		select {
		case <-s.settleCh:
			return s.result()
		case msg, ok := <-s.conn.Read():
			if !ok {
				return relayclient.ErrConnectionClosed
			}
			if !s.observe(msg) && other != nil {
				other(msg)
			}
		case err := <-s.conn.Errors():
			return err
		case <-s.conn.Closed():
			return relayclient.ErrConnectionClosed
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			if !s.challenged() {
				return &LoginError{Relay: s.relay, Reason: LoginRelayRefused, Detail: refusal + " (the relay sent no AUTH challenge)"}
			}
			return &LoginError{Relay: s.relay, Reason: LoginRelayRefused, Detail: "no answer to the login"}
		}
	}
}

// loginFirst is for a long-lived connection, before its first REQ or
// EVENT: if the relay challenges within challengeWait, it waits for the
// login to resolve. Messages read meanwhile go to other (nil drops them).
// A relay that never challenges costs challengeWait, and only with a signer.
func (s *relaySession) loginFirst(ctx context.Context, other func(wire.SubscriptionResponse)) error {
	if s.signer == nil {
		return nil
	}
	wait := time.NewTimer(challengeWait)
	defer wait.Stop()
	for {
		select {
		case <-s.askedCh:
			return s.awaitLogin(ctx, "", other)
		case msg, ok := <-s.conn.Read():
			if !ok {
				return relayclient.ErrConnectionClosed
			}
			if !s.observe(msg) && other != nil {
				other(msg)
			}
		case err := <-s.conn.Errors():
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-wait.C:
			return nil
		}
	}
}

// publish sends ev and waits for its OK, returning the relay's message on
// acceptance. If the relay asks for a login first, it logs in and sends ev
// once more.
func (s *relaySession) publish(ctx context.Context, ev *nip01.Event) (string, error) {
	retried := false
	for {
		ok, err := s.awaitOK(ctx, ev)
		if err != nil {
			return "", err
		}
		switch {
		case ok.Accepted:
			return ok.Message, nil
		case refusedForLogin(ok.Message) && !retried:
			if err := s.awaitLogin(ctx, ok.Message, nil); err != nil {
				return "", err
			}
			retried = true
			continue
		case s.loggedIn() && (refusedForLogin(ok.Message) || strings.HasPrefix(ok.Message, restrictedPrefix)):
			return "", &LoginError{Relay: s.relay, Reason: LoginRelayRefused, Detail: ok.Message}
		}
		return "", fmt.Errorf("relay rejected event: %s", ok.Message)
	}
}

// awaitOK sends ev and returns the relay's OK for it, within 10s.
func (s *relaySession) awaitOK(ctx context.Context, ev *nip01.Event) (*wire.OkSubscriptionResponse, error) {
	if !s.conn.Send(ev) {
		return nil, errors.New("connection closed during send")
	}
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case response := <-s.conn.Read():
			if s.observe(response) {
				continue
			}
			switch r := response.(type) {
			case *wire.OkSubscriptionResponse:
				if r.EventID == ev.ID {
					return r, nil
				}
			case *wire.NoticeSubscriptionResponse:
				log.Warn().Str("notice", r.Message).Msg("received notice while publishing")
			}
		case err := <-s.conn.Errors():
			return nil, err
		case <-s.conn.Closed():
			return nil, errors.New("connection closed")
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout.C:
			return nil, errors.New("timeout waiting for OK")
		}
	}
}

// read runs one REQ to EOSE. If the relay asks for a login, it logs in
// and runs the REQ once more. restricted reports a "restricted:" CLOSED.
func (s *relaySession) read(ctx context.Context, filters *nip01.SubscriptionFilterGroup) (events []*nip01.Event, restricted bool, err error) {
	loggedInBefore := s.loggedIn()
	events, closedMsg, closed, err := s.subscribeOnce(ctx, filters)
	if err != nil {
		return nil, false, err
	}

	switch {
	case closed && refusedForLogin(closedMsg):
		if err := s.awaitLogin(ctx, closedMsg, nil); err != nil {
			return nil, false, err
		}
	case s.challenged() && !loggedInBefore && (!closed || strings.HasPrefix(closedMsg, restrictedPrefix)):
		// The REQ raced the login, so the relay may have answered it
		// anonymously. Run it again once logged in.
		if err := s.awaitLogin(ctx, closedMsg, nil); err != nil {
			var le *LoginError
			if errors.As(err, &le) && !closed {
				log.Warn().Err(err).Msg("login failed; keeping the anonymous result")
				return events, false, nil
			}
			return nil, false, err
		}
	case closed:
		return events, strings.HasPrefix(closedMsg, restrictedPrefix), nil
	default:
		return events, false, nil
	}

	events, closedMsg, closed, err = s.subscribeOnce(ctx, filters)
	if err != nil {
		return nil, false, err
	}
	if closed && refusedForLogin(closedMsg) {
		return nil, false, &LoginError{Relay: s.relay, Reason: LoginRelayRefused, Detail: closedMsg}
	}
	return events, closed && strings.HasPrefix(closedMsg, restrictedPrefix), nil
}

// subscribeOnce drives one REQ to EOSE or CLOSED.
func (s *relaySession) subscribeOnce(ctx context.Context, filters *nip01.SubscriptionFilterGroup) (events []*nip01.Event, closedMsg string, closed bool, err error) {
	subID := uuid.NewString()
	if !s.conn.SubscribeWithID(subID, filters) {
		return nil, "", false, relayclient.ErrConnectionClosed
	}
	for {
		select {
		case res, ok := <-s.conn.Read():
			if !ok {
				return events, "", false, nil
			}
			if s.observe(res) {
				continue
			}
			switch m := res.(type) {
			case *wire.EventSubscriptionResponse:
				if m.SubscriptionID == subID {
					events = append(events, m.Event)
				}
			case *wire.EOSESubscriptionResponse:
				if m.SubscriptionID == subID {
					s.conn.CloseSubscription(subID)
					return events, "", false, nil
				}
			case *wire.ClosedSubscriptionResponse:
				if m.SubscriptionID == subID {
					return events, m.Message, true, nil
				}
			}
		case err := <-s.conn.Errors():
			return nil, "", false, err
		case <-ctx.Done():
			return nil, "", false, ctx.Err()
		}
	}
}
