package ncli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip49"
)

func TestScanImportKey(t *testing.T) {
	id, _ := client.GenerateIdentity()
	enc, err := nip49.Encrypt(id.PrivKeyHex, "pw")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, in, want string
		skipped        int
	}{
		{"nsec", id.Nsec + "\n", id.Nsec, 0},
		{"hex upper", strings.ToUpper(id.PrivKeyHex), strings.ToUpper(id.PrivKeyHex), 0},
		{"ncryptsec", enc, enc, 0},
		{"third line, padded", "\n  junk\n\n\t " + id.Nsec + "  \r\n", id.Nsec, 1},
		{"first valid wins", "nsec1bad\n" + id.Nsec + "\n" + enc + "\n", id.Nsec, 1},
		{"pubkey-only", id.Npub + "\n", "", 1},
		{"zero hex", strings.Repeat("0", 64), "", 1},
		{"truncated ncryptsec", enc[:len(enc)-10], "", 1},
		{"empty", "", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, skipped, err := scanImportKey(strings.NewReader(tc.in))
			if err != nil || got != tc.want || skipped != tc.skipped {
				t.Fatalf("scanImportKey() = (%q, %d, %v), want (%q, %d)", got, skipped, err, tc.want, tc.skipped)
			}
		})
	}
}

func TestDecryptNcryptsec(t *testing.T) {
	// NIP-49's own test vector (spec layout, with key_security byte).
	const spec = "ncryptsec1qgg9947rlpvqu76pj5ecreduf9jxhselq2nae2kghhvd5g7dgjtcxfqtd67p9m0w57lspw8gsq6yphnm8623nsl8xn9j4jdzz84zm3frztj3z7s35vpzmqf6ksu8r89qk5z2zxfmu5gv8th8wclt0h4p"
	got, err := decryptNcryptsec(spec, "nostr")
	if want := "3501454135014541350145413501453fefb02227e449e57cf4d3a3ce05378683"; err != nil || got != want {
		t.Fatalf("decryptNcryptsec(spec vector) = (%q, %v), want %q", got, err, want)
	}
	if _, err := decryptNcryptsec(spec, "wrong"); err == nil {
		t.Fatal("decryptNcryptsec(wrong password) error = nil")
	}

	// nmilat's own (key_security-less) form.
	id, _ := client.GenerateIdentity()
	enc, _ := nip49.Encrypt(id.PrivKeyHex, "pw")
	if got, err := decryptNcryptsec(enc, "pw"); err != nil || got != id.PrivKeyHex {
		t.Fatalf("decryptNcryptsec(nmilat) = (%q, %v), want the key", got, err)
	}
}

type importResult struct {
	Npub          string `json:"npub"`
	Label         string `json:"label"`
	Status        string `json:"status"`
	PreviousLabel string `json:"previous_label"`
	SkippedLines  int    `json:"skipped_lines"`
	Code          string `json:"code"`
	Input         string `json:"input"`
	Error         string `json:"error"`
}

// runImport runs `id import --json` with stdin and returns the parsed
// stdout (success) or stderr (failure) object plus the exit code.
func runImport(t *testing.T, bin string, env []string, stdin string, args ...string) (importResult, int, string) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"id", "import", "--json"}, args...)...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	raw := stdout.String()
	if code != 0 {
		raw = stderr.String()
	}
	var res importResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, raw)
	}
	return res, code, stdout.String() + stderr.String()
}

func TestIDImportContract(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env := append(vaultTestEnv(t), "NCLI_VAULT_PASSWORD=pw")

	id, _ := client.GenerateIdentity()
	other, _ := client.GenerateIdentity()

	res, code, _ := runImport(t, bin, env, "\njunk\n  "+id.Nsec+"  \n"+other.Nsec+"\n", "--label", "alice")
	if code != 0 || res.Status != "imported" || res.Label != "alice" || res.Npub != id.Npub || res.SkippedLines != 1 {
		t.Fatalf("first import = %+v (exit %d), want imported alice, 1 skipped", res, code)
	}

	for _, args := range [][]string{nil, {"--label", "ALICE"}} {
		res, code, _ = runImport(t, bin, env, id.PrivKeyHex, args...)
		if code != 0 || res.Status != "unchanged" || res.Label != "alice" {
			t.Fatalf("re-import %v = %+v (exit %d), want unchanged alice", args, res, code)
		}
	}

	res, code, _ = runImport(t, bin, env, id.Nsec, "--label", "bob")
	if code != 5 || res.Code != "conflict" || res.Input != "alice" {
		t.Fatalf("import under new label = %+v (exit %d), want conflict naming alice", res, code)
	}

	res, code, _ = runImport(t, bin, env, id.Nsec, "--label", "bob", "--force")
	if code != 0 || res.Status != "relabeled" || res.Label != "bob" || res.PreviousLabel != "alice" {
		t.Fatalf("--force = %+v (exit %d), want relabeled alice -> bob", res, code)
	}

	res, code, _ = runImport(t, bin, env, other.Nsec, "--label", "Bob")
	if code != 5 || res.Code != "conflict" || res.Input != "Bob" {
		t.Fatalf("label held by another key = %+v (exit %d), want conflict", res, code)
	}

	// --file: first valid key, even past junk lines.
	path := filepath.Join(t.TempDir(), "keys.txt")
	if err := os.WriteFile(path, []byte("# my key\n\n"+other.PrivKeyHex+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	res, code, _ = runImport(t, bin, env, "", "--file", path, "--label", "carol")
	if code != 0 || res.Status != "imported" || res.Npub != other.Npub {
		t.Fatalf("--file import = %+v (exit %d), want imported carol", res, code)
	}

	entries := listVault(t, bin, env)
	if len(entries) != 2 {
		t.Fatalf("vault holds %d entries, want 2 (a key is never stored twice)", len(entries))
	}

	// Errors never echo key material.
	res, code, out := runImport(t, bin, env, "", id.Nsec)
	if code != 2 || res.Code != "usage" || strings.Contains(out, id.Nsec) {
		t.Fatalf("positional key = %+v (exit %d), want usage error without the key echoed", res, code)
	}
	res, code, out = runImport(t, bin, env, "nsec1garbage\n"+id.Npub+"\n")
	if code != 3 || res.Code != "invalid_input" || res.Input != "" || strings.Contains(out, "garbage") {
		t.Fatalf("no valid key = %+v (exit %d), want invalid_input with no input", res, code)
	}
}

func TestIDImportNcryptsec(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env := append(vaultTestEnv(t), "NCLI_VAULT_PASSWORD=pw")

	id, _ := client.GenerateIdentity()
	enc, err := nip49.Encrypt(id.PrivKeyHex, "secret")
	if err != nil {
		t.Fatal(err)
	}

	res, code, _ := runImport(t, bin, env, enc)
	if code != 2 || res.Code != "usage" {
		t.Fatalf("no NCLI_IMPORT_PASSWORD = %+v (exit %d), want usage", res, code)
	}

	res, code, _ = runImport(t, bin, append(env, "NCLI_IMPORT_PASSWORD=wrong"), enc)
	if code != 7 || res.Code != "auth" {
		t.Fatalf("wrong password = %+v (exit %d), want auth", res, code)
	}

	res, code, _ = runImport(t, bin, append(env, "NCLI_IMPORT_PASSWORD=secret"), enc, "--label", "enc")
	if code != 0 || res.Status != "imported" || res.Npub != id.Npub {
		t.Fatalf("ncryptsec import = %+v (exit %d), want imported", res, code)
	}
}

func listVault(t *testing.T, bin string, env []string) []map[string]any {
	t.Helper()
	cmd := exec.Command(bin, "id", "list", "--json")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("id list: %v", err)
	}
	var list struct {
		Identities []map[string]any `json:"identities"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatal(err)
	}
	return list.Identities
}
