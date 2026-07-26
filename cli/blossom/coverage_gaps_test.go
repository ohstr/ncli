package blossom

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohstr/ncli/client"
)

// TestBlossomDownload_MultiServerFallback proves "download" actually
// falls back through the configured server list in order, not just the
// first one -- the documented behavior GetFromServers provides. The blob
// only exists on the second configured server; the first must 404 and
// download must still succeed via the second.
func TestBlossomDownload_MultiServerFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)

	missing := newFakeBlossomServer() // never receives the blob
	defer missing.Close()
	has := newFakeBlossomServer()
	defer has.Close()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.txt")
	content := []byte("fallback content")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatal(err)
	}
	// Upload only to "has", bypassing the (not yet configured) default list.
	out, _ := r.mustRun("blossom", "upload", "--identity", r.nsec, "--server", has.URL, filePath, "--json")
	var uploadReport struct {
		Results []struct {
			Sha256 string `json:"sha256"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &uploadReport); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	hash := uploadReport.Results[0].Sha256

	// Configure both, "missing" first, so the client must fall back.
	r.mustRun("blossom", "servers", "add", missing.URL)
	r.mustRun("blossom", "servers", "add", has.URL)

	outPath := filepath.Join(dir, "downloaded.txt")
	r.mustRun("blossom", "download", hash, "-o", outPath)
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("downloaded content = %q, want %q", got, content)
	}
}

// TestBlossomDownload_AcceptsURIAndURLForms proves "download" actually
// resolves a "blossom:" URI and a hash-shaped server URL end to end
// (normalizeDownloadTarget is unit-tested in isolation, but this proves
// the whole command works with these input shapes against a real server).
func TestBlossomDownload_AcceptsURIAndURLForms(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	server := newFakeBlossomServer()
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.txt")
	content := []byte("uri/url download test")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatal(err)
	}
	out, _ := r.mustRun("blossom", "upload", "--identity", r.nsec, filePath, "--json")
	var uploadReport struct {
		Results []struct {
			Sha256 string `json:"sha256"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &uploadReport); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	hash := uploadReport.Results[0].Sha256

	t.Run("blossom uri", func(t *testing.T) {
		outPath := filepath.Join(dir, "via-uri.txt")
		r.mustRun("blossom", "download", "blossom:"+hash+".txt", "-o", outPath)
		got, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("downloaded content = %q, want %q", got, content)
		}
	})

	t.Run("hash-shaped url", func(t *testing.T) {
		outPath := filepath.Join(dir, "via-url.txt")
		r.mustRun("blossom", "download", server.URL+"/"+hash+".txt", "-o", outPath)
		got, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("downloaded content = %q, want %q", got, content)
		}
	})
}

// TestBlossomDownload_StdoutPiping proves "-o -" streams the exact blob
// bytes to stdout with nothing else mixed in (previously only checked
// manually in a smoke test, never automated).
func TestBlossomDownload_StdoutPiping(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	server := newFakeBlossomServer()
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.txt")
	content := []byte("piped to stdout, byte for byte")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatal(err)
	}
	out, _ := r.mustRun("blossom", "upload", "--identity", r.nsec, filePath, "--json")
	var uploadReport struct {
		Results []struct {
			Sha256 string `json:"sha256"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &uploadReport); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	hash := uploadReport.Results[0].Sha256

	stdout, _ := r.mustRun("blossom", "download", hash, "-o", "-")
	if stdout != string(content) {
		t.Errorf("stdout = %q, want exactly %q with no extra narration mixed in", stdout, content)
	}
}

// TestBlossomUpload_Optimize proves --optimize actually reaches PUT
// /media (not /upload): the fake server's /media handler requires a
// VerbMedia-scoped auth token, so if the client mistakenly hit /upload
// with it (or vice versa), the request would fail with a verb mismatch.
func TestBlossomUpload_Optimize(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	server := newFakeBlossomServer()
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(filePath, []byte("optimize me"), 0644); err != nil {
		t.Fatal(err)
	}

	out, _ := r.mustRun("blossom", "upload", "--identity", r.nsec, "--optimize", filePath, "--json")
	var got struct {
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if got.Succeeded != 1 || got.Failed != 0 {
		t.Fatalf("report = %+v, want succeeded=1 failed=0", got)
	}
}

// TestBlossomMirror_SucceededTransferButInvalidResponse is mirror's
// counterpart to upload's identical test: Mirror shares the exact same
// "PUT succeeded but the response was malformed/invalid" SDK behavior as
// Upload, but had zero dedicated test proving the CLI handles it the same
// way.
func TestBlossomMirror_SucceededTransferButInvalidResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}

	for _, hook := range []string{"malformed body", "invalid descriptor"} {
		t.Run(hook, func(t *testing.T) {
			r := newRunner(t)
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Write([]byte("mirror source content"))
			}))
			defer source.Close()

			target := newFakeBlossomServer()
			defer target.Close()
			if hook == "malformed body" {
				target.SetHooks(fakeServerHooks{MalformedResponseBody: true})
			} else {
				target.SetHooks(fakeServerHooks{InvalidDescriptor: true})
			}
			r.mustRun("blossom", "servers", "add", target.URL)

			out, _, code := r.run("blossom", "mirror", "--identity", r.nsec, source.URL, "--json")
			if code == 0 {
				t.Fatal("exit code = 0, want non-zero")
			}
			if !strings.Contains(out, "already sent") {
				t.Errorf("report = %s, want it to include the \"already sent to the server\" disclaimer", out)
			}
			if len(target.blobs) != 1 {
				t.Errorf("target has %d blobs, want 1 (the transfer succeeded even though the CLI reported an error)", len(target.blobs))
			}
		})
	}
}

// TestBlossomMirror_MultiServerFanOut and TestBlossomRm_MultiServerFanOut
// prove mirror/rm fan out to every configured server, not just one --
// previously only proven for upload.
func TestBlossomMirror_MultiServerFanOut(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Write([]byte("fan out mirror content"))
	}))
	defer source.Close()

	server1 := newFakeBlossomServer()
	defer server1.Close()
	server2 := newFakeBlossomServer()
	defer server2.Close()
	r.mustRun("blossom", "servers", "add", server1.URL)
	r.mustRun("blossom", "servers", "add", server2.URL)

	out, _ := r.mustRun("blossom", "mirror", "--identity", r.nsec, source.URL, "--json")
	var got struct {
		Attempted int `json:"attempted"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if got.Attempted != 2 || got.Succeeded != 2 || got.Failed != 0 {
		t.Fatalf("report = %+v, want attempted=2 succeeded=2 failed=0", got)
	}
}

func TestBlossomRm_MultiServerFanOut(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	server1 := newFakeBlossomServer()
	defer server1.Close()
	server2 := newFakeBlossomServer()
	defer server2.Close()
	r.mustRun("blossom", "servers", "add", server1.URL)
	r.mustRun("blossom", "servers", "add", server2.URL)

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(filePath, []byte("rm fan out test"), 0644); err != nil {
		t.Fatal(err)
	}
	out, _ := r.mustRun("blossom", "upload", "--identity", r.nsec, filePath, "--json")
	var uploadReport struct {
		Results []struct {
			Sha256 string `json:"sha256"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &uploadReport); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	hash := uploadReport.Results[0].Sha256

	out2, _ := r.mustRun("blossom", "rm", hash, "--identity", r.nsec, "--yes", "--json")
	var got struct {
		Attempted int `json:"attempted"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	}
	if err := json.Unmarshal([]byte(out2), &got); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out2)
	}
	if got.Attempted != 2 || got.Succeeded != 2 || got.Failed != 0 {
		t.Fatalf("report = %+v, want attempted=2 succeeded=2 failed=0", got)
	}
}

// TestBlossomServers_DuplicateAddReportsFalse, RemoveAbsentReportsFalse,
// and AddRejectsBadScheme cover basic CRUD edge cases that had no
// blossom-specific test (only the analogous relay ones exist, in a
// different package).
func TestBlossomServers_DuplicateAddReportsFalse(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)

	out1, _ := r.mustRun("blossom", "servers", "add", "https://dup.example", "--json")
	var first struct {
		Added bool `json:"added"`
	}
	if err := json.Unmarshal([]byte(out1), &first); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out1)
	}
	if !first.Added {
		t.Fatal("first add: added = false, want true")
	}

	out2, _ := r.mustRun("blossom", "servers", "add", "https://dup.example", "--json")
	var second struct {
		Added bool `json:"added"`
	}
	if err := json.Unmarshal([]byte(out2), &second); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out2)
	}
	if second.Added {
		t.Error("second add of the same server: added = true, want false")
	}
}

func TestBlossomServers_RemoveAbsentReportsFalse(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)

	out, _ := r.mustRun("blossom", "servers", "remove", "https://never-added.example", "--json")
	var got struct {
		Removed bool `json:"removed"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if got.Removed {
		t.Error("removed = true, want false for a server that was never added")
	}
}

func TestBlossomServers_AddRejectsBadScheme(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)

	_, stderr, code := r.run("blossom", "servers", "add", "ftp://bad-scheme.example")
	if code != 3 { // common.CodeInvalidInput's exit code
		t.Errorf("exit code = %d, want 3 (invalid_input)", code)
	}
	if !strings.Contains(stderr, "http") {
		t.Errorf("stderr = %q, want it to mention the http/https scheme requirement", stderr)
	}
}

// TestBlossomList_PositionalPubkeyUnauthenticated proves "list <pubkey>"
// works without --identity at all (BUD-12 listing doesn't require auth)
// -- every other list test uses --identity, leaving this path untested.
func TestBlossomList_PositionalPubkeyUnauthenticated(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	server := newFakeBlossomServer()
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(filePath, []byte("positional pubkey test"), 0644); err != nil {
		t.Fatal(err)
	}
	r.mustRun("blossom", "upload", "--identity", r.nsec, filePath)

	idOut, _ := r.mustRun("id", r.nsec, "--json")
	var identity struct {
		PubHex string `json:"pub_hex"`
	}
	if err := json.Unmarshal([]byte(idOut), &identity); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, idOut)
	}
	if identity.PubHex == "" {
		t.Fatal("could not resolve a pubkey via `ncli id`")
	}

	out, _ := r.mustRun("blossom", "list", identity.PubHex, "--json")
	var descriptors []struct {
		Sha256 string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(out), &descriptors); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if len(descriptors) != 1 {
		t.Errorf("list <pubkey> without --identity returned %d descriptors, want 1", len(descriptors))
	}
}

// TestBlossomList_ResolvesNpubPositionalArg is the regression test for gap
// 1: "list <npub>" used to send the npub string itself as the BUD-12
// GET /list/<pubkey> path segment instead of resolving it to a hex pubkey
// first, so it always came back empty (no server understands an npub).
// This proves the positional identifier is resolved via
// client.ResolveIdentifier -- upload under a freshly generated identity's
// nsec, then list by that same identity's npub (not the raw hex) and
// confirm the uploaded blob is found.
func TestBlossomList_ResolvesNpubPositionalArg(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	server := newFakeBlossomServer()
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	identity, err := client.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(filePath, []byte("npub resolution test"), 0644); err != nil {
		t.Fatal(err)
	}
	r.mustRun("blossom", "upload", "--identity", identity.Nsec, filePath)

	out, _ := r.mustRun("blossom", "list", identity.Npub, "--json")
	var descriptors []struct {
		Sha256 string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(out), &descriptors); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if len(descriptors) != 1 {
		t.Errorf("list <npub> returned %d descriptors, want 1 -- the npub must resolve to the identity's actual pubkey, not be sent to the server literally", len(descriptors))
	}
}

// TestBlossomList_UnresolvableIdentifierIsInvalidInput proves a malformed
// positional identifier (garbage, not npub/hex/nsec/nprofile/nip-05
// shaped) fails fast with invalid_input rather than being silently sent to
// the server as a literal (and useless) pubkey string.
func TestBlossomList_UnresolvableIdentifierIsInvalidInput(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	server := newFakeBlossomServer()
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	_, stderr, code := r.run("blossom", "list", "not-a-valid-identifier")
	if code != 3 { // common.CodeInvalidInput's exit code
		t.Errorf("exit code = %d, want 3 (invalid_input)", code)
	}
	if stderr == "" {
		t.Error("stderr is empty, want an error message")
	}
}

// TestBlossomList_PaginationFlagsAreSentToServer proves --cursor/--limit/
// --since/--until are actually encoded onto the GET /list/<pubkey>
// request, not just accepted as flags and silently dropped.
func TestBlossomList_PaginationFlagsAreSentToServer(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	server := newFakeBlossomServer()
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	r.mustRun("blossom", "list", "--identity", r.nsec, "--cursor", "abc123", "--limit", "7", "--since", "100", "--until", "200")

	got := server.LastListQuery()
	if got.Get("cursor") != "abc123" {
		t.Errorf("cursor = %q, want abc123", got.Get("cursor"))
	}
	if got.Get("limit") != "7" {
		t.Errorf("limit = %q, want 7", got.Get("limit"))
	}
	if got.Get("since") != "100" {
		t.Errorf("since = %q, want 100", got.Get("since"))
	}
	if got.Get("until") != "200" {
		t.Errorf("until = %q, want 200", got.Get("until"))
	}
}

// TestBlossomReport_ServerRejection proves report surfaces a server-side
// rejection as a non-zero exit instead of silently succeeding.
func TestBlossomReport_ServerRejection(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	server := newFakeBlossomServer()
	server.SetHooks(fakeServerHooks{FailStatus: 500})
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	hash := strings.Repeat("a", 64)
	_, _, code := r.run("blossom", "report", hash, "--identity", r.nsec, "--type", "spam", "--reason", "test")
	if code == 0 {
		t.Fatal("exit code = 0, want non-zero when the server rejects the report")
	}
}
