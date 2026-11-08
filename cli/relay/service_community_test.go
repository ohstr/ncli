package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip29"
	"github.com/ohstr/nmilat/nip86"
	"github.com/ohstr/nmilat/nipB7"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/wire"
	"github.com/stretchr/testify/require"
)

func communityRelayConfig(t *testing.T) RelayConfig {
	up := true
	return RelayConfig{
		Nip11: nip11.Metadata{
			PubKey: testPubKey, PrivKey: testPrivKey, Self: testPubKey, URL: huddleRelayURL,
		},
		Membership: &MembershipConfig{Enabled: true},
		Groups:     &GroupsConfig{MembershipNotices: &MembershipNoticesConfig{Enabled: true}},
		HTTPBridge: &HTTPBridgeConfig{
			Query:  &QueryConfig{Enabled: true},
			Events: &EventsConfig{Enabled: true},
			Count:  &CountConfig{Enabled: true},
		},
		Presence: &PresenceConfig{Enabled: true},
		Blossom: &BlossomConfig{
			Enabled:     true,
			MembersOnly: BlossomMembersOnly{Upload: &up},
			Storage:     BlossomStorage{Dir: filepath.Join(t.TempDir(), "blobs")},
		},
	}
}

// connectAs opens a WebSocket to ts and logs in as privKey.
func connectAs(t *testing.T, ts *httptest.Server, privKey string) *relayclient.Connection {
	t.Helper()
	u, err := url.Parse("ws" + strings.TrimPrefix(ts.URL, "http"))
	require.NoError(t, err)
	conn, err := relayclient.Connect(context.Background(), u)
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	authAs(t, conn, privKey)
	return conn
}

// reqEvents runs one REQ on conn and returns the events before EOSE.
func reqEvents(t *testing.T, conn *relayclient.Connection, filter *nip01.SubscriptionFilter) []*nip01.Event {
	t.Helper()
	conn.Outgoing() <- wire.NewRequestPacket("q", nip01.NewSubscriptionFilterGroup(filter))
	var events []*nip01.Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case res := <-conn.Read():
			switch r := res.(type) {
			case *wire.EventSubscriptionResponse:
				events = append(events, r.Event)
			case *wire.EOSESubscriptionResponse:
				return events
			case *wire.ClosedSubscriptionResponse:
				t.Fatalf("REQ closed: %s", r.Message)
			}
		case <-deadline:
			t.Fatal("timed out waiting for EOSE")
		}
	}
}

func publishAs(t *testing.T, conn *relayclient.Connection, ev *nip01.Event, privKey string) *wire.OkSubscriptionResponse {
	t.Helper()
	require.NoError(t, ev.Sign(privKey))
	ok, err := conn.Publish(context.Background(), ev)
	require.NoError(t, err)
	return ok
}

// postNIP98 POSTs body to ts+path signed by privKey.
func postNIP98(t *testing.T, ts *httptest.Server, path, privKey string, body []byte) *http.Response {
	t.Helper()
	target := ts.URL + path
	header, err := common.GenerateNIP98Header(privKey, target, http.MethodPost, body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", header)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func countAs(t *testing.T, ts *httptest.Server, privKey string, body string) int64 {
	t.Helper()
	resp := postNIP98(t, ts, "/count", privKey, []byte(body))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got struct{ Count int64 }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	return got.Count
}

func blossomToken(t *testing.T, privKey, verb, sha string) string {
	t.Helper()
	ev := nipB7.NewAuthorization(nipB7.AuthorizationParams{
		Verb: verb, Expiration: time.Now().Add(time.Minute), Servers: []string{"relay.test"}, Hashes: []string{sha},
	})
	require.NoError(t, ev.Sign(privKey))
	h, err := nipB7.EncodeAuthHeader(ev)
	require.NoError(t, err)
	return h
}

func TestScenario_CommunityRelay(t *testing.T) {
	ts := bootScenario(t, communityRelayConfig(t))
	enrollMember(t, ts.URL, scenarioMemberPub)

	member := connectAs(t, ts, scenarioMemberPriv)
	agent := connectAs(t, ts, scenarioAgentPriv)

	const groupID = "closed-room"
	require.True(t, publishAs(t, member, nip29.NewCreateGroup(scenarioMemberPub, groupID), scenarioMemberPriv).Accepted)
	put := nip29.NewPutUser(scenarioMemberPub, groupID, scenarioAgentPub)
	require.True(t, publishAs(t, member, put, scenarioMemberPriv).Accepted)

	notices := &nip01.SubscriptionFilter{Kinds: []int{44100}, Tags: map[string][]string{"p": {scenarioAgentPub}}}

	t.Run("the agent gets its seat notice", func(t *testing.T) {
		got := reqEvents(t, agent, notices)
		require.Len(t, got, 1)
		require.Equal(t, testPubKey, got[0].PubKey)
		require.Contains(t, got[0].Tags, []string{"h", groupID})
		require.Contains(t, got[0].Tags, []string{"e", put.ID})
	})

	t.Run("nobody else reads it", func(t *testing.T) {
		require.Empty(t, reqEvents(t, member, notices))
		body := `[{"kinds":[44100],"#p":["` + scenarioAgentPub + `"]}]`
		require.EqualValues(t, 0, countAs(t, ts, scenarioMemberPriv, body))
		require.EqualValues(t, 1, countAs(t, ts, scenarioAgentPriv, body))
	})

	t.Run("a client can't forge one", func(t *testing.T) {
		forged := &nip01.Event{Kind: 44100, CreatedAt: uint64(time.Now().Unix()), Tags: [][]string{{"p", scenarioAgentPub}, {"h", groupID}}}
		require.False(t, publishAs(t, member, forged, scenarioMemberPriv).Accepted)

		viaHTTP := &nip01.Event{Kind: 44101, CreatedAt: uint64(time.Now().Unix()), Tags: [][]string{{"p", scenarioAgentPub}, {"h", groupID}}}
		require.NoError(t, viaHTTP.Sign(scenarioMemberPriv))
		body, err := json.Marshal(viaHTTP)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, postNIP98(t, ts, "/events", scenarioMemberPriv, body).StatusCode)
	})

	t.Run("POST /events applies the group's rules", func(t *testing.T) {
		post := func(privKey string, ev *nip01.Event) *http.Response {
			t.Helper()
			require.NoError(t, ev.Sign(privKey))
			body, err := json.Marshal(ev)
			require.NoError(t, err)
			return postNIP98(t, ts, "/events", privKey, body)
		}
		chat := func() *nip01.Event {
			return &nip01.Event{Kind: 9, Content: "hi", CreatedAt: uint64(time.Now().Unix()), Tags: [][]string{{"h", groupID}}}
		}

		resp := post(blossomBannedPriv, chat())
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "a non-member must not post into a closed group")

		resp = post(scenarioMemberPriv, nip29.NewPutUser(scenarioMemberPub, groupID, scenarioAgentPub))
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		msg, _ := io.ReadAll(resp.Body)
		require.Contains(t, string(msg), "restricted:", "moderation goes over a WebSocket")

		ev := chat()
		resp = post(scenarioAgentPriv, ev)
		require.Equal(t, http.StatusOK, resp.StatusCode, "a member posts to its own group")
		got := reqEvents(t, member, &nip01.SubscriptionFilter{IDs: []string{ev.ID}})
		require.Len(t, got, 1)
	})

	t.Run("presence over /query", func(t *testing.T) {
		const absent = "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee6"
		resp := postNIP98(t, ts, "/query", scenarioMemberPriv, []byte(`[{"kinds":[40902],"authors":["`+scenarioAgentPub+`","`+absent+`"]}]`))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var got []nip01.Event
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		status := map[string]string{}
		for _, ev := range got {
			require.Equal(t, testPubKey, ev.PubKey)
			status[ev.Tags[0][1]] = ev.Content
		}
		require.Equal(t, map[string]string{scenarioAgentPub: "online", absent: "offline"}, status)
	})

	t.Run("blossom on the relay origin", func(t *testing.T) {
		blob := []byte("a file for the room")
		sum := sha256.Sum256(blob)
		sha := hex.EncodeToString(sum[:])
		upload := func(path, privKey string) *http.Response {
			req, err := http.NewRequest(http.MethodPut, ts.URL+path, bytes.NewReader(blob))
			require.NoError(t, err)
			req.Header.Set("Authorization", blossomToken(t, privKey, nipB7.VerbUpload, sha))
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })
			return resp
		}

		require.Equal(t, http.StatusForbidden, upload("/upload", scenarioAgentPriv).StatusCode, "a non-member must not upload")
		resp := upload("/media/upload", scenarioMemberPriv)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var d nipB7.BlobDescriptor
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&d))
		require.Equal(t, "https://relay.test/media/"+sha+".bin", d.URL, "an untyped blob's URL ends in .bin")

		get, err := http.Get(ts.URL + "/media/" + sha + ".txt")
		require.NoError(t, err)
		defer func() { _ = get.Body.Close() }()
		got, _ := io.ReadAll(get.Body)
		require.Equal(t, blob, got)

		notBlob, err := http.Get(ts.URL + "/media/readme.txt")
		require.NoError(t, err)
		_ = notBlob.Body.Close()
		require.Equal(t, http.StatusNotFound, notBlob.StatusCode)
	})
}

// Two more keys for the Blossom tests: one enrolled and then banned, one
// named in nip86.admins without being a member.
const (
	blossomBannedPriv = "0000000000000000000000000000000000000000000000000000000000000003"
	blossomAdminPriv  = "0000000000000000000000000000000000000000000000000000000000000004"
)

// blossomDo sends one Blossom request to ts as privKey ("" sends none),
// with a token for verb and sha.
func blossomDo(t *testing.T, ts *httptest.Server, method, path, privKey, verb, sha string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, body)
	require.NoError(t, err)
	if privKey != "" {
		req.Header.Set("Authorization", blossomToken(t, privKey, verb, sha))
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func blossomUpload(t *testing.T, ts *httptest.Server, privKey string, blob []byte) *http.Response {
	t.Helper()
	return blossomDo(t, ts, http.MethodPut, "/upload", privKey, nipB7.VerbUpload, sha256Hex(blob), bytes.NewReader(blob))
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// nip86As calls the relay's NIP-86 management API on ts as privKey and
// fails the test unless the call succeeds.
func nip86As(t *testing.T, ts *httptest.Server, privKey, method string, params ...any) {
	t.Helper()
	raw := make([]json.RawMessage, 0, len(params))
	for _, p := range params {
		b, err := json.Marshal(p)
		require.NoError(t, err)
		raw = append(raw, b)
	}
	body, err := json.Marshal(nip86.Request{Method: method, Params: raw})
	require.NoError(t, err)
	header, err := common.GenerateNIP98Header(privKey, ts.URL+"/", http.MethodPost, body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", nip86.ContentType)
	req.Header.Set("Authorization", header)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got nip86.Response
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Empty(t, got.Error, "%s failed", method)
}

// TestScenario_RelayBlossomPolicy checks who the relay lets upload,
// download and delete: a NIP-86 ban outranks membership, only an uploader
// or an admin deletes, and maxBlobBytes holds.
func TestScenario_RelayBlossomPolicy(t *testing.T) {
	cfg := communityRelayConfig(t)
	adminPub := pubkeyOf(t, blossomAdminPriv)
	cfg.Nip86 = &Nip86Config{Enabled: true, Admins: []string{adminPub}}
	// The ban is saved beside the store, so give it one.
	cfg.RelayNotesDb = filepath.Join(t.TempDir(), "relay.db")
	cfg.Blossom.MembersOnly.Download = true
	cfg.Blossom.MaxBlobBytes = 64
	ts := bootScenario(t, cfg)
	enrollMember(t, ts.URL, scenarioMemberPub)
	enrollMember(t, ts.URL, pubkeyOf(t, blossomBannedPriv))

	get := func(privKey, sha string) int {
		return blossomDo(t, ts, http.MethodGet, "/media/"+sha, privKey, nipB7.VerbGet, sha, nil).StatusCode
	}
	del := func(privKey, sha string) int {
		return blossomDo(t, ts, http.MethodDelete, "/media/"+sha, privKey, nipB7.VerbDelete, sha, nil).StatusCode
	}

	t.Run("a banned member may neither upload nor download", func(t *testing.T) {
		before := []byte("uploaded before the ban")
		require.Equal(t, http.StatusOK, blossomUpload(t, ts, blossomBannedPriv, before).StatusCode)
		require.Equal(t, http.StatusOK, get(blossomBannedPriv, sha256Hex(before)))

		nip86As(t, ts, testPrivKey, nip86.MethodBanPubkey, pubkeyOf(t, blossomBannedPriv), "spam")

		require.Equal(t, http.StatusForbidden, blossomUpload(t, ts, blossomBannedPriv, []byte("after the ban")).StatusCode)
		require.Equal(t, http.StatusForbidden, get(blossomBannedPriv, sha256Hex(before)))
		require.Equal(t, http.StatusOK, get(scenarioMemberPriv, sha256Hex(before)), "the blob itself stays up for members")
	})

	t.Run("download needs a member's get token", func(t *testing.T) {
		blob := []byte("members only download")
		require.Equal(t, http.StatusOK, blossomUpload(t, ts, scenarioMemberPriv, blob).StatusCode)
		require.Equal(t, http.StatusUnauthorized, get("", sha256Hex(blob)))
		require.Equal(t, http.StatusForbidden, get(scenarioAgentPriv, sha256Hex(blob)))
		require.Equal(t, http.StatusOK, get(blossomAdminPriv, sha256Hex(blob)), "an admin counts as a member")
	})

	t.Run("only the uploader or an admin deletes", func(t *testing.T) {
		for name, admin := range map[string]string{"nip86.admins": blossomAdminPriv, "relay key": testPrivKey} {
			blob := []byte("deleted by the " + name)
			sha := sha256Hex(blob)
			require.Equal(t, http.StatusOK, blossomUpload(t, ts, scenarioMemberPriv, blob).StatusCode)
			require.Equal(t, http.StatusForbidden, del(scenarioAgentPriv, sha), "a non-uploader non-admin must not delete")
			require.Equal(t, http.StatusOK, get(scenarioMemberPriv, sha))
			require.Equal(t, http.StatusOK, del(admin, sha), "the %s deletes any blob", name)
			require.Equal(t, http.StatusNotFound, get(scenarioMemberPriv, sha))
		}

		own := []byte("deleted by its uploader")
		require.Equal(t, http.StatusOK, blossomUpload(t, ts, scenarioMemberPriv, own).StatusCode)
		require.Equal(t, http.StatusOK, del(scenarioMemberPriv, sha256Hex(own)))
		require.Equal(t, http.StatusNotFound, get(scenarioMemberPriv, sha256Hex(own)))
	})

	t.Run("maxBlobBytes", func(t *testing.T) {
		big := bytes.Repeat([]byte("x"), 65)
		require.Equal(t, http.StatusRequestEntityTooLarge, blossomUpload(t, ts, scenarioMemberPriv, big).StatusCode, "declared Content-Length")
		// No Content-Length: the relay must still stop reading at the cap.
		chunked := blossomDo(t, ts, http.MethodPut, "/upload", scenarioMemberPriv, nipB7.VerbUpload, sha256Hex(big), io.MultiReader(bytes.NewReader(big)))
		require.Equal(t, http.StatusRequestEntityTooLarge, chunked.StatusCode, "chunked body")
		require.Equal(t, http.StatusNotFound, get(scenarioMemberPriv, sha256Hex(big)))
		require.Equal(t, http.StatusOK, blossomUpload(t, ts, scenarioMemberPriv, big[:64]).StatusCode, "exactly the cap is allowed")
	})
}

// stalledUpload is an upload that has sent part of its body and waits.
type stalledUpload struct {
	pw   *io.PipeWriter
	rest []byte
	tr   *http.Transport

	// answered is closed once code/err hold the relay's answer.
	answered chan struct{}
	code     int
	err      error

	once   sync.Once
	result int
	failed error
}

var errUploadAborted = errors.New("held upload aborted")

// startStalledUpload starts an upload as privKey that sends the first
// bytes of blob and then waits for finish. It asks for 100-continue, so
// the body is only sent once the relay holds a slot for it and reads it;
// an upload refused a slot gets its 429 at once instead of the relay
// blocking on a body nobody sends. Its cleanup, registered after
// bootScenario's, runs before the server's Close and ends the upload, so
// a failing test never leaves Close waiting on it.
func startStalledUpload(t *testing.T, ts *httptest.Server, privKey string, blob []byte) *stalledUpload {
	t.Helper()
	pr, pw := io.Pipe()
	s := &stalledUpload{
		pw:       pw,
		rest:     blob[4:],
		tr:       &http.Transport{ExpectContinueTimeout: time.Minute},
		answered: make(chan struct{}),
	}
	// Closing the body closes the pipe, so a write to an upload the
	// client gave up on fails instead of blocking.
	body := struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(blob[:4]), pr), pr}
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/upload", body)
	require.NoError(t, err)
	req.Header.Set("Authorization", blossomToken(t, privKey, nipB7.VerbUpload, sha256Hex(blob)))
	req.Header.Set("Expect", "100-continue")
	t.Cleanup(func() { _, _ = s.end(true) })
	go func() {
		defer close(s.answered)
		resp, err := (&http.Client{Transport: s.tr}).Do(req)
		if err != nil {
			s.err = err
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		s.code = resp.StatusCode
	}()
	return s
}

// end sends the rest of the body (or, with abort, fails it) and waits for
// the relay's answer. Only the first call does anything.
func (s *stalledUpload) end(abort bool) (int, error) {
	s.once.Do(func() {
		if abort {
			_ = s.pw.CloseWithError(errUploadAborted)
		} else {
			go func() {
				_, _ = s.pw.Write(s.rest)
				_ = s.pw.Close()
			}()
		}
		select {
		case <-s.answered:
			s.result, s.failed = s.code, s.err
		case <-time.After(10 * time.Second):
			_ = s.pw.CloseWithError(errUploadAborted)
			s.failed = errors.New("the relay never answered the held upload")
		}
		s.tr.CloseIdleConnections()
	})
	return s.result, s.failed
}

// finish sends the rest of the body and returns the relay's answer.
func (s *stalledUpload) finish(t *testing.T) int {
	t.Helper()
	code, err := s.end(false)
	require.NoError(t, err, "the held upload failed")
	return code
}

// probeUpload uploads as privKey with a token for other bytes than it
// sends: it gets past the in-flight limits to a 403, or stops at them
// with a 429, and never stores anything.
func probeUpload(t *testing.T, ts *httptest.Server, privKey string) int {
	t.Helper()
	body := []byte("probe")
	return blossomDo(t, ts, http.MethodPut, "/upload", privKey, nipB7.VerbUpload, sha256Hex([]byte("not the probe")), bytes.NewReader(body)).StatusCode
}

// holdUpload starts an upload as holder and returns once the relay counts
// it in flight, which is when a probe as prober is first turned away
// with a 429. A probe that raced the holder for the one slot gets the
// holder a 429 instead; that holder is ended and started again.
func holdUpload(t *testing.T, ts *httptest.Server, holder, prober string, blob []byte) *stalledUpload {
	t.Helper()
	for range 10 {
		s := startStalledUpload(t, ts, holder, blob)
	probe:
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			select {
			case <-s.answered:
				code, err := s.end(true)
				require.NoError(t, err, "the held upload failed")
				require.Equal(t, http.StatusTooManyRequests, code, "the held upload ended early")
				break probe
			default:
			}
			code := probeUpload(t, ts, prober)
			if code == http.StatusTooManyRequests {
				return s
			}
			require.Equal(t, http.StatusForbidden, code)
			time.Sleep(5 * time.Millisecond)
		}
	}
	t.Fatal("the held upload never counted as in flight")
	return nil
}

// TestScenario_RelayBlossomUploadLimits checks the relay applies
// maxUploadsPerSigner and maxConcurrentUploads to uploads in flight.
func TestScenario_RelayBlossomUploadLimits(t *testing.T) {
	blob := []byte("a slow upload held open")

	t.Run("maxUploadsPerSigner", func(t *testing.T) {
		cfg := communityRelayConfig(t)
		cfg.Blossom.MaxUploadsPerSigner = 1
		ts := bootScenario(t, cfg)
		enrollMember(t, ts.URL, scenarioMemberPub)

		held := holdUpload(t, ts, scenarioMemberPriv, scenarioMemberPriv, blob)
		require.Equal(t, http.StatusTooManyRequests, blossomUpload(t, ts, scenarioMemberPriv, []byte("second")).StatusCode)
		require.Equal(t, http.StatusOK, blossomUpload(t, ts, testPrivKey, []byte("another signer")).StatusCode, "the cap is per signer")
		require.Equal(t, http.StatusOK, held.finish(t))
		require.Equal(t, http.StatusOK, blossomUpload(t, ts, scenarioMemberPriv, []byte("third")).StatusCode, "the slot is released")
	})

	t.Run("maxConcurrentUploads", func(t *testing.T) {
		cfg := communityRelayConfig(t)
		cfg.Blossom.MaxConcurrentUploads = 1
		ts := bootScenario(t, cfg)
		enrollMember(t, ts.URL, scenarioMemberPub)

		held := holdUpload(t, ts, scenarioMemberPriv, testPrivKey, blob)
		require.Equal(t, http.StatusTooManyRequests, blossomUpload(t, ts, testPrivKey, []byte("another signer")).StatusCode, "the cap is relay-wide")
		require.Equal(t, http.StatusOK, held.finish(t))
		require.Equal(t, http.StatusOK, blossomUpload(t, ts, testPrivKey, []byte("another signer")).StatusCode, "the slot is released")
	})
}

func TestCheckCommunityConfig(t *testing.T) {
	valid := func() RelayConfig {
		c := communityRelayConfig(t)
		return c
	}
	require.NoError(t, func() error { c := valid(); return checkCommunityConfig(&c) }())

	for name, tc := range map[string]func(*RelayConfig){
		"notices without a key":       func(c *RelayConfig) { c.Nip11.PrivKey = "" },
		"notices with one kind twice": func(c *RelayConfig) { c.Groups.MembershipNotices.Added = 44101 },
		"presence without /query":     func(c *RelayConfig) { c.HTTPBridge.Query = nil },
		"blossom without a host":      func(c *RelayConfig) { c.Nip11.URL = "" },
		"blossom at the root":         func(c *RelayConfig) { c.Blossom.BlobPrefix = "/" },
		"blossom over /query":         func(c *RelayConfig) { c.Blossom.UploadPaths = []string{"/query"} },
		"s3 without a bucket": func(c *RelayConfig) {
			c.Blossom.Storage = BlossomStorage{Kind: "s3", S3: BlossomS3{Endpoint: "minio:9000"}}
		},
		"an unknown storage kind": func(c *RelayConfig) { c.Blossom.Storage.Kind = "ftp" },
	} {
		t.Run(name, func(t *testing.T) {
			c := valid()
			tc(&c)
			require.Error(t, checkCommunityConfig(&c))
		})
	}

	t.Run("defaults", func(t *testing.T) {
		c := valid()
		k, ok := c.memberNotices()
		require.True(t, ok)
		require.Equal(t, 44100, k.Added)
		require.Equal(t, 44101, k.Removed)
		require.Equal(t, 40902, c.presenceKind())
		require.ElementsMatch(t, []int{44100, 44101, 40902}, c.relayAuthoredKinds())
		require.Equal(t, "https://relay.test", c.blossomPublicURL())
		require.Equal(t, "/media", c.Blossom.blobPrefix())
		require.Equal(t, []string{"/upload", "/media/upload"}, c.Blossom.uploadPaths())
	})

	t.Run("loads from yaml", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
groups:
  membershipNotices:
    enabled: true
    added: 45000
presence:
  enabled: true
httpBridge:
  count:
    enabled: true
blossom:
  enabled: true
  membersOnly:
    upload: true
    download: true
  storage:
    kind: s3
    s3:
      endpoint: minio:9000
      bucket: media
`)
		k, _ := cfg.memberNotices()
		require.Equal(t, 45000, k.Added)
		require.True(t, cfg.HTTPBridge.Count.Enabled)
		require.True(t, cfg.Blossom.MembersOnly.Download)
		require.Equal(t, "media", cfg.Blossom.Storage.S3.Bucket)
	})
}
