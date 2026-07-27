package blossom

import (
	"testing"

	"github.com/ohstr/ncli/client"
)

// TestBlossomMirror_AgainstServerRequiringHashScope confirms followup
// issue #3 (integration/agent-eval/followup/issues.md): a live
// hzrd149/blossom-server rejected every "ncli blossom mirror" call with
// 403 "Auth event is missing required x tag for PUT /mirror", even though
// upload/rm (whose buildAuth calls in upload.go/rm.go do pass a hash
// scope) succeeded against the very same server with the very same
// identity. The root cause: mirror.go's own buildAuth call
// (`buildAuth(privKeyHex, pubKeyHex, nipB7.VerbUpload, nil, ttl)`) hard-
// codes a nil hashes slice instead of scoping the token to the source
// blob's sha256, so its BUD-11 authorization never carries an "x" tag at
// all -- unlike rm.go, which scopes its delete token to the hash it was
// given.
//
// This reproduces that rejection locally: pre-seed a blob (standing in for
// "already uploaded"), then mirror it back onto the very same server under
// the very same identity -- exactly the repro from issues.md -- against a
// fakeBlossomServer configured to require hash-scoped mirror tokens the
// same way the real server does (see fakeserver_test.go's
// RequireHashOnMirror hook).
func TestBlossomMirror_AgainstServerRequiringHashScope(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}

	identity, err := client.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	r := &ncliRunner{t: t, bin: buildTestBinary(t), env: blossomTestEnv(t), nsec: identity.Nsec}

	server := newFakeBlossomServer()
	defer server.Close()
	server.SetHooks(fakeServerHooks{RequireHashOnMirror: true})

	hash := server.put(identity.PubKeyHex, []byte("already-uploaded blob"), "text/plain")
	sourceURL := server.URL + "/" + hash

	out, errOut, code := r.run("blossom", "mirror", "--identity", r.nsec, "--server", server.URL, sourceURL, "--json")
	if code != 0 {
		t.Fatalf("mirror of an already-uploaded blob back onto the same server/identity failed (confirms followup issue #3 -- mirror.go's BUD-11 auth token is missing the required \"x\" hash tag): exit=%d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
}
