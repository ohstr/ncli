package blossom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nipB7"
	"github.com/spf13/cobra"
)

// list --all fails only when no server answered, and then takes the most
// actionable code: a retryable network failure on one server is not hidden
// behind another server's missing list endpoint.
func TestListAllServers_Classification(t *testing.T) {
	server := func(status int) string {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if status == http.StatusOK {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("[]"))
				return
			}
			w.WriteHeader(status)
		}))
		t.Cleanup(ts.Close)
		return ts.URL
	}
	pubkey := strings.Repeat("a", 64)
	cases := []struct {
		name     string
		statuses []int
		want     common.ErrorCode // "" = success
	}{
		{"one answers with nothing, another lacks list", []int{http.StatusNotFound, http.StatusOK}, ""},
		{"no list endpoint anywhere", []int{http.StatusNotFound, http.StatusMethodNotAllowed}, common.CodeUnsupported},
		{"a network failure beside a missing endpoint", []int{http.StatusNotFound, http.StatusServiceUnavailable}, common.CodeNetwork},
		{"a network failure listed last still wins", []int{http.StatusNotImplemented, http.StatusNotFound, http.StatusBadGateway}, common.CodeNetwork},
		{"an auth failure beside a missing endpoint", []int{http.StatusNotFound, http.StatusUnauthorized}, common.CodeAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var servers []string
			for _, s := range tc.statuses {
				servers = append(servers, server(s))
			}
			got, err := listAllServers(context.Background(), newHTTPClient(5*time.Second), servers, pubkey, nipB7.ListQuery{}, nil)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v, want success", err)
				}
				if len(got) != 0 {
					t.Fatalf("got %d blobs, want none", len(got))
				}
				return
			}
			if err == nil {
				t.Fatal("err = nil, want a failure")
			}
			ce, ok := classifyListError(&cobra.Command{Use: "test"}, pubkey, err).(*common.CLIError)
			if !ok {
				t.Fatalf("classified as %T, want *common.CLIError", err)
			}
			if ce.Code != tc.want {
				t.Errorf("code = %q, want %q (%v)", ce.Code, tc.want, ce.Err)
			}
			for _, s := range servers {
				if !strings.Contains(ce.Error(), s) {
					t.Errorf("error %q does not name %s", ce.Error(), s)
				}
			}
		})
	}
}
