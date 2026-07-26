package blossom

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipB7"
)

// fakeBlob is one stored blob in a fakeBlossomServer's in-memory store.
type fakeBlob struct {
	data        []byte
	contentType string
	uploaded    int64
}

// fakeServerHooks lets a test force specific server-side behavior that a
// well-behaved server would never trigger on its own, matching the
// failure modes nipB7/client's own tests already exercise against a real
// HTTP transport: a 402 payment-required response, a successful transfer
// whose response body is malformed JSON, one whose body is well-formed
// JSON but fails BlobDescriptor.Validate(), or a bare status-code failure.
type fakeServerHooks struct {
	PaymentRequired       bool
	MalformedResponseBody bool
	InvalidDescriptor     bool
	FailStatus            int

	// ResponseDelay, if set, sleeps this long after a successful auth
	// check but before responding to an upload -- lets a test simulate a
	// slow server/network so that several sequential (file, server)
	// upload calls take real, cumulative wall-clock time, the exact
	// condition a single batch-wide auth token (instead of one per call)
	// would eventually expire under.
	ResponseDelay time.Duration

	// TruncateDownload, if true, declares a larger Content-Length than
	// the bytes actually written on a GET, so the client sees a
	// truncated transfer (io.ErrUnexpectedEOF) instead of a clean
	// download -- for testing that a failed download doesn't leave a
	// partial file behind.
	TruncateDownload bool
}

// fakeBlossomServer is a minimal, spec-correct Blossom server for tests:
// real BUD-11 authorization verification via nipB7.VerifyAuthorization
// (not a hand-rolled stub), an in-memory blob store, and Hooks for
// injecting the failure modes above -- so the CLI layer can be exercised
// against the same scenarios nipB7/client's own tests cover, end to end
// through the compiled binary.
type fakeBlossomServer struct {
	*httptest.Server

	mu            sync.Mutex
	blobs         map[string]*fakeBlob
	byUser        map[string][]string // pubkey hex -> hashes, upload order
	lastListQuery url.Values          // query params of the most recent GET /list/<pubkey>, for asserting the client encoded them
	hooks         fakeServerHooks
}

// LastListQuery returns the query parameters of the most recent GET
// /list/<pubkey> request this server received, for tests asserting that
// --cursor/--limit/--since/--until are actually sent, not just accepted
// as flags.
func (s *fakeBlossomServer) LastListQuery() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastListQuery
}

// Hooks returns a snapshot of the server's current failure-injection
// hooks. Reading through the same mutex SetHooks writes through means a
// test goroutine calling SetHooks and a request-handling goroutine
// reading Hooks are properly ordered -- a bare exported struct field
// written directly by a test (the original shape of this type) raced
// under `go test -race` even though the two accesses never actually
// overlapped in wall-clock time, since nothing established a happens-
// before edge between them.
func (s *fakeBlossomServer) Hooks() fakeServerHooks {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hooks
}

// SetHooks replaces the server's failure-injection hooks.
func (s *fakeBlossomServer) SetHooks(h fakeServerHooks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = h
}

func newFakeBlossomServer() *fakeBlossomServer {
	s := &fakeBlossomServer{
		blobs:  make(map[string]*fakeBlob),
		byUser: make(map[string][]string),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/media", s.handleUpload)
	mux.HandleFunc("/mirror", s.handleMirror)
	mux.HandleFunc("/report", s.handleReport)
	mux.HandleFunc("/list/", s.handleList)
	mux.HandleFunc("/", s.handleBlob)
	// NewUnstartedServer + an explicit Start(), not httptest.NewServer:
	// NewServer spawns the request-serving goroutine *inside* itself,
	// before it returns -- so "s.Server = httptest.NewServer(mux)" writes
	// s.Server strictly after that goroutine already exists, with no
	// happens-before edge covering the assignment itself. A handler
	// reading s.URL (= s.Server.URL, since Server is embedded) then races
	// against that assignment under the race detector, even though in
	// every real run the assignment finishes long before any request can
	// physically arrive. Assigning the unstarted server first and calling
	// Start() ourselves, second, puts the goroutine-spawning "go"
	// statement strictly after our assignment in program order on the
	// same goroutine, which the Go memory model does guarantee orders
	// correctly relative to the spawned goroutine.
	s.Server = httptest.NewUnstartedServer(mux)
	s.Server.Start()
	return s
}

// put stores data directly (bypassing HTTP) under pubkey, for tests that
// need a pre-existing blob (e.g. to mirror or download) without going
// through an actual upload first. Returns the stored hash.
func (s *fakeBlossomServer) put(pubkey string, data []byte, contentType string) string {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs[hash] = &fakeBlob{data: data, contentType: contentType, uploaded: time.Now().Unix()}
	s.byUser[pubkey] = append(s.byUser[pubkey], hash)
	return hash
}

func (s *fakeBlossomServer) requireAuth(w http.ResponseWriter, r *http.Request, verb, hash string, requireHash bool) (*nipB7.Authorization, bool) {
	auth, err := nipB7.VerifyAuthorization(r, nipB7.VerifyParams{Verb: verb, Hash: hash, RequireHash: requireHash})
	if err != nil {
		nipB7.WriteError(w, http.StatusUnauthorized, err.Error())
		return nil, false
	}
	return auth, true
}

func (s *fakeBlossomServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	hooks := s.Hooks()

	if r.Method != http.MethodPut {
		nipB7.WriteError(w, http.StatusMethodNotAllowed, "")
		return
	}
	if hooks.FailStatus != 0 {
		nipB7.WriteError(w, hooks.FailStatus, "injected failure")
		return
	}

	verb := nipB7.VerbUpload
	if r.URL.Path == "/media" {
		verb = nipB7.VerbMedia
	}
	auth, ok := s.requireAuth(w, r, verb, "", false)
	if !ok {
		return
	}

	if hooks.ResponseDelay > 0 {
		time.Sleep(hooks.ResponseDelay)
	}

	if hooks.PaymentRequired {
		w.Header().Set(nipB7.HeaderCashu, "cashuAtest")
		w.Header().Set(nipB7.HeaderLightning, "lnbctest")
		nipB7.WriteError(w, http.StatusPaymentRequired, "payment required")
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		nipB7.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	contentType := r.Header.Get("Content-Type")
	hash := s.put(auth.PubKey, data, contentType)

	w.WriteHeader(http.StatusCreated)
	s.writeDescriptor(w, hash, int64(len(data)), contentType, hooks)
}

func (s *fakeBlossomServer) handleMirror(w http.ResponseWriter, r *http.Request) {
	hooks := s.Hooks()

	if r.Method != http.MethodPut {
		nipB7.WriteError(w, http.StatusMethodNotAllowed, "")
		return
	}
	if hooks.FailStatus != 0 {
		nipB7.WriteError(w, hooks.FailStatus, "injected failure")
		return
	}
	auth, ok := s.requireAuth(w, r, nipB7.VerbUpload, "", false)
	if !ok {
		return
	}
	if hooks.PaymentRequired {
		w.Header().Set(nipB7.HeaderCashu, "cashuAtest")
		nipB7.WriteError(w, http.StatusPaymentRequired, "payment required")
		return
	}

	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		nipB7.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	// A dedicated client, not http.Get/http.DefaultClient: two
	// fakeBlossomServer instances mirroring from the same source
	// concurrently otherwise share package-level connection-pool state,
	// which the race detector correctly flags as unsynchronized.
	resp, err := (&http.Client{}).Get(body.URL)
	if err != nil {
		nipB7.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		nipB7.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}

	contentType := resp.Header.Get("Content-Type")
	hash := s.put(auth.PubKey, data, contentType)

	w.WriteHeader(http.StatusCreated)
	s.writeDescriptor(w, hash, int64(len(data)), contentType, hooks)
}

func (s *fakeBlossomServer) writeDescriptor(w http.ResponseWriter, hash string, size int64, contentType string, hooks fakeServerHooks) {
	if hooks.MalformedResponseBody {
		w.Write([]byte("not json"))
		return
	}
	d := nipB7.BlobDescriptor{
		URL:      s.URL + "/" + hash,
		Sha256:   hash,
		Size:     size,
		Type:     contentType,
		Uploaded: time.Now().Unix(),
	}
	if hooks.InvalidDescriptor {
		d.URL = "" // fails BlobDescriptor.Validate() despite a 2xx transfer
	}
	json.NewEncoder(w).Encode(d)
}

func (s *fakeBlossomServer) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		nipB7.WriteError(w, http.StatusMethodNotAllowed, "")
		return
	}
	if hooks := s.Hooks(); hooks.FailStatus != 0 {
		nipB7.WriteError(w, hooks.FailStatus, "injected failure")
		return
	}
	var event nip01.Event
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		nipB7.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := nipB7.ValidateReport(&event); err != nil {
		nipB7.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *fakeBlossomServer) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		nipB7.WriteError(w, http.StatusMethodNotAllowed, "")
		return
	}
	if hooks := s.Hooks(); hooks.FailStatus != 0 {
		nipB7.WriteError(w, hooks.FailStatus, "injected failure")
		return
	}
	// Auth is optional for list per BUD-12; only verify it if present, so
	// public/unauthenticated "ncli blossom list <pubkey>" keeps working.
	if r.Header.Get("Authorization") != "" {
		if _, ok := s.requireAuth(w, r, nipB7.VerbList, "", false); !ok {
			return
		}
	}

	pubkey := strings.TrimPrefix(r.URL.Path, "/list/")

	s.mu.Lock()
	s.lastListQuery = r.URL.Query()
	hashes := append([]string(nil), s.byUser[pubkey]...)
	descriptors := make([]nipB7.BlobDescriptor, 0, len(hashes))
	for _, h := range hashes {
		b := s.blobs[h]
		if b == nil {
			continue
		}
		descriptors = append(descriptors, nipB7.BlobDescriptor{
			URL: s.URL + "/" + h, Sha256: h, Size: int64(len(b.data)), Type: b.contentType, Uploaded: b.uploaded,
		})
	}
	s.mu.Unlock()

	nipB7.SortDescending(descriptors)
	json.NewEncoder(w).Encode(descriptors)
}

func (s *fakeBlossomServer) handleBlob(w http.ResponseWriter, r *http.Request) {
	hooks := s.Hooks()
	if hooks.FailStatus != 0 {
		nipB7.WriteError(w, hooks.FailStatus, "injected failure")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	hash := path
	if i := strings.LastIndex(path, "."); i >= 0 {
		hash = path[:i]
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.mu.Lock()
		b := s.blobs[hash]
		s.mu.Unlock()
		if b == nil {
			nipB7.WriteError(w, http.StatusNotFound, "blob not found")
			return
		}
		w.Header().Set("Content-Type", b.contentType)
		contentLength := len(b.data)
		if hooks.TruncateDownload {
			// Declare more bytes than we actually write, so the client
			// sees a truncated transfer (io.ErrUnexpectedEOF) instead of
			// a clean download.
			contentLength += 64
		}
		w.Header().Set("Content-Length", strconv.Itoa(contentLength))
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Write(b.data)

	case http.MethodDelete:
		// BUD-11: delete tokens "should be scoped to exactly this hash" --
		// enforced here via RequireHash so an unscoped delete token (valid
		// for "any blob") is rejected, matching a strict real server.
		auth, ok := s.requireAuth(w, r, nipB7.VerbDelete, hash, true)
		if !ok {
			return
		}
		s.mu.Lock()
		_, existed := s.blobs[hash]
		delete(s.blobs, hash)
		if hashes, found := s.byUser[auth.PubKey]; found {
			filtered := hashes[:0]
			for _, h := range hashes {
				if h != hash {
					filtered = append(filtered, h)
				}
			}
			s.byUser[auth.PubKey] = filtered
		}
		s.mu.Unlock()
		if !existed {
			nipB7.WriteError(w, http.StatusNotFound, "blob not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		nipB7.WriteError(w, http.StatusMethodNotAllowed, "")
	}
}
