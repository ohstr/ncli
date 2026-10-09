package keyresolve

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nipLS"
	"github.com/spf13/cobra"
)

func TestSignerError(t *testing.T) {
	cmd := &cobra.Command{}
	cases := map[string]struct {
		err  error
		code int
	}{
		"denied":       {nipLS.Deny("no rule matches"), 7},
		"invalid":      {nipLS.Invalid("malformed event JSON"), 3},
		"other remote": {&nipLS.Error{Reason: "sign failed"}, 1},
		"hung up":      {fmt.Errorf("sign: %w", nipLS.ErrConnClosed), 6},
		"deadline":     {fmt.Errorf("sign: %w", context.DeadlineExceeded), 6},
		"plain":        {errors.New("boom"), 1},
	}
	for name, c := range cases {
		if got := common.ExitCode(SignerError(cmd, "events.json", c.err)); got != c.code {
			t.Errorf("%s: exit %d, want %d", name, got, c.code)
		}
	}
}

func TestResolveSignerLocalAndErrors(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	id, err := client.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	s, closeFn, err := ResolveSigner(cmd, true, id.Nsec)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if _, ok := s.(*client.LocalSigner); !ok || s.PubKey() != id.PubKeyHex {
		t.Fatalf("nsec resolved to %T %s", s, s.PubKey())
	}

	npub, _ := nip19.EncodePublicKey(id.PubKeyHex)
	if _, _, err := ResolveSigner(cmd, true, npub); common.ExitCode(err) != 7 {
		t.Errorf("pubkey-only identity: %v", err)
	}
	if _, _, err := ResolveSigner(cmd, true, "bunker+unix://relative.sock"); common.ExitCode(err) != 3 {
		t.Errorf("relative socket: %v", err)
	}
	if _, _, err := ResolveSigner(cmd, true, "bunker+unix:///nonexistent/ncli.sock"); common.ExitCode(err) != 6 {
		t.Errorf("no signer: %v", err)
	}
}
