package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nipB7"
	"github.com/stretchr/testify/require"
)

// TestScenario_RelayBlossomAtRoot checks the relay serves blobs at the root
// of its origin (BUD-01), keeps /media/<sha> working, still answers / with
// NIP-11, and turns away list, mirror and media with a reason instead of a
// bare 404 or a redirect.
func TestScenario_RelayBlossomAtRoot(t *testing.T) {
	ts := bootScenario(t, communityRelayConfig(t))
	enrollMember(t, ts.URL, scenarioMemberPub)

	blob := []byte("served from the root")
	sha := sha256Hex(blob)
	require.Equal(t, http.StatusOK, blossomUpload(t, ts, scenarioMemberPriv, blob).StatusCode)

	for _, path := range []string{"/" + sha, "/" + sha + ".txt", "/media/" + sha, "/media/" + sha + ".txt"} {
		t.Run("GET and HEAD "+strings.Replace(path, sha, "<sha>", 1), func(t *testing.T) {
			resp := blossomDo(t, ts, http.MethodGet, path, "", "", "", nil)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			got, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, blob, got)

			head := blossomDo(t, ts, http.MethodHead, path, "", "", "", nil)
			require.Equal(t, http.StatusOK, head.StatusCode)
			require.EqualValues(t, len(blob), head.ContentLength)
		})
	}

	t.Run("OPTIONS at the root", func(t *testing.T) {
		resp := blossomDo(t, ts, http.MethodOptions, "/"+sha, "", "", "", nil)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
		require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	})

	t.Run("DELETE at the root", func(t *testing.T) {
		gone := []byte("deleted from the root")
		goneSha := sha256Hex(gone)
		require.Equal(t, http.StatusOK, blossomUpload(t, ts, scenarioMemberPriv, gone).StatusCode)
		require.Equal(t, http.StatusForbidden, blossomDo(t, ts, http.MethodDelete, "/"+goneSha, scenarioAgentPriv, nipB7.VerbDelete, goneSha, nil).StatusCode, "the root path keeps the delete rules")
		require.Equal(t, http.StatusOK, blossomDo(t, ts, http.MethodDelete, "/"+goneSha, scenarioMemberPriv, nipB7.VerbDelete, goneSha, nil).StatusCode)
		require.Equal(t, http.StatusNotFound, blossomDo(t, ts, http.MethodGet, "/"+goneSha, "", "", "", nil).StatusCode)
		require.Equal(t, http.StatusNotFound, blossomDo(t, ts, http.MethodGet, "/media/"+goneSha, "", "", "", nil).StatusCode)
	})

	t.Run("/ is still the relay", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
		require.NoError(t, err)
		req.Header.Set("Accept", nip11.ContentTypeHeader)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var md nip11.Metadata
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&md))
		require.Equal(t, testPubKey, md.PubKey)
	})

	t.Run("anything else at the root is not found", func(t *testing.T) {
		for _, path := range []string{"/foo", "/" + strings.ToUpper(sha), "/" + sha[:63], "/" + sha + "/x", "/" + sha + ".toolongext"} {
			require.Equal(t, http.StatusNotFound, blossomDo(t, ts, http.MethodGet, path, "", "", "", nil).StatusCode, path)
		}
		require.Equal(t, http.StatusNotFound, blossomDo(t, ts, http.MethodPut, "/"+sha, "", "", "", bytes.NewReader(blob)).StatusCode, "PUT is not a blob method")
	})

	t.Run("list, mirror and media say they are not supported", func(t *testing.T) {
		for _, c := range []struct{ method, path, reason string }{
			{http.MethodGet, "/list/" + scenarioMemberPub, "list is not supported on this server"},
			{http.MethodPut, "/mirror", "mirror is not supported on this server"},
			{http.MethodPut, "/media", "media optimization is not supported"},
			{http.MethodHead, "/media", "media optimization is not supported"},
		} {
			resp := blossomDo(t, ts, c.method, c.path, scenarioMemberPriv, nipB7.VerbUpload, sha, strings.NewReader(`{"url":"https://example.com/x"}`))
			require.Equal(t, http.StatusNotFound, resp.StatusCode, "%s %s", c.method, c.path)
			require.Equal(t, c.reason, resp.Header.Get(nipB7.HeaderReason), "%s %s", c.method, c.path)
		}
	})
}

// TestBlossomUnsupportedRoutesSkipTakenPaths: a configured Blossom path
// wins over the not-supported answer, and registering both would panic.
func TestBlossomUnsupportedRoutesSkipTakenPaths(t *testing.T) {
	b := &BlossomConfig{BlobPrefix: "/list", UploadPaths: []string{"/mirror", "/media"}}
	require.Empty(t, b.unsupportedRoutes())
	require.Len(t, (&BlossomConfig{}).unsupportedRoutes(), 3)
}
