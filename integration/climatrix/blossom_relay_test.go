package climatrix

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// minioImage is pinned by digest: MinIO no longer publishes images on
// Docker Hub, and Chainguard's free tier only tags :latest.
const minioImage = "cgr.dev/chainguard/minio@sha256:a1ac4589a5f8136b616e667b48a0bea9719bb10bd45799ea946f24bbedfafde4"

// MinIO is an S3-compatible store running in Docker, with one bucket.
type MinIO struct {
	Endpoint  string // host:port
	AccessKey string
	SecretKey string
	Bucket    string
	client    *minio.Client
}

// StartMinIO runs MinIO in Docker and creates a bucket; skips when Docker
// isn't available.
func StartMinIO(t *testing.T) *MinIO {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	m := &MinIO{AccessKey: "climatrix", SecretKey: "climatrix-secret", Bucket: "ncli-media"}
	port := allocPort(t)
	name := fmt.Sprintf("climatrix-minio-%d-%d", os.Getpid(), port)
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-p", fmt.Sprintf("%d:9000", port),
		"-e", "MINIO_ROOT_USER="+m.AccessKey, "-e", "MINIO_ROOT_PASSWORD="+m.SecretKey,
		minioImage, "server", "/data").CombinedOutput()
	if err != nil {
		t.Skipf("can't start %s: %v\n%s", minioImage, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	m.Endpoint = fmt.Sprintf("%s:%d", dockerHost(), port)
	m.client, err = minio.New(m.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(m.AccessKey, m.SecretKey, "")})
	if err != nil {
		t.Fatal(err)
	}
	var lastErr error
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		lastErr = m.client.MakeBucket(ctx, m.Bucket, minio.MakeBucketOptions{})
		cancel()
		if lastErr == nil {
			return m
		}
	}
	t.Fatalf("minio at %s never created bucket %s: %v (set CLIMATRIX_DOCKER_HOST if docker ports aren't on localhost)", m.Endpoint, m.Bucket, lastErr)
	return nil
}

// Has reports whether the bucket holds key.
func (m *MinIO) Has(t *testing.T, key string) bool {
	t.Helper()
	_, err := m.client.StatObject(context.Background(), m.Bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return true
	}
	if minio.ToErrorResponse(err).Code == "NoSuchKey" {
		return false
	}
	t.Fatalf("stat %s: %v", key, err)
	return false
}

// TestBlossomRelay drives `ncli blossom` against Blossom served by a real
// `ncli relay` process, once with blobs in a directory and once in MinIO.
func TestBlossomRelay(t *testing.T) {
	needsRelay(t)

	t.Run("dir", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "blobs")
		testBlossomRelay(t, map[string]any{"kind": "dir", "dir": dir}, nil, func(t *testing.T, sha string) bool {
			_, err := os.Stat(filepath.Join(dir, sha[:2], sha))
			return err == nil
		})
	})

	t.Run("s3", func(t *testing.T) {
		m := StartMinIO(t)
		storage := map[string]any{"kind": "s3", "s3": map[string]any{
			"endpoint": m.Endpoint, "bucket": m.Bucket,
			// The keys come from the environment; a NCLI_* variable only
			// overrides a key the file already has.
			"accessKey": "", "secretKey": "",
		}}
		env := map[string]string{
			"NCLI_BLOSSOM_STORAGE_S3_ACCESSKEY": m.AccessKey,
			"NCLI_BLOSSOM_STORAGE_S3_SECRETKEY": m.SecretKey,
		}
		testBlossomRelay(t, storage, env, func(t *testing.T, sha string) bool { return m.Has(t, sha) })
	})
}

// testBlossomRelay runs the scenario on a relay storing blobs in storage;
// stored reports whether the store holds a blob.
func testBlossomRelay(t *testing.T, storage map[string]any, env map[string]string, stored func(*testing.T, string) bool) {
	t.Helper()
	const maxBlob = 1024
	r := StartRelayEnv(t, "", map[string]any{
		"membership": map[string]any{"enabled": true},
		"blossom": map[string]any{
			"enabled":      true,
			"maxBlobBytes": maxBlob,
			"membersOnly":  map[string]any{"upload": true},
			"storage":      storage,
		},
	}, env)
	// Uploads go to the relay's origin (PUT /upload); blobs are served,
	// deleted and looked up under its blob prefix, so download and rm
	// name that as the server.
	// ohstr/ncli#239: download and rm against srv 404 today (the client
	// asks for /<sha> at the root). Once fixed, use srv for them and drop
	// media.
	srv := "http" + strings.TrimPrefix(r.URL, "ws")
	media := srv + "/media"
	alice, eve := A(t, "alice"), A(t, "eve")
	addMember(t, r, alice.PubHex)

	e := NewEnv(t)
	content := "hello from the relay's own origin\n"
	file := e.WriteFile("hello.txt", content)
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])

	t.Run("upload returns a URL on the relay origin", func(t *testing.T) {
		var rep struct {
			Succeeded int
			Results   []struct {
				SHA256 string `json:"sha256"`
				URL    string `json:"url"`
			}
		}
		e.MustOK(t, "blossom", "upload", file, "--identity", alice.Nsec, "--server", srv, "--json").JSON(t, &rep)
		if rep.Succeeded != 1 || len(rep.Results) != 1 || rep.Results[0].SHA256 != hash {
			t.Fatalf("upload: %+v", rep)
		}
		if want := srv + "/media/" + hash; !strings.HasPrefix(rep.Results[0].URL, want) {
			t.Errorf("url = %q, want %s[.ext]", rep.Results[0].URL, want)
		}
		if !stored(t, hash) {
			t.Errorf("blob %s is not in the configured store", hash)
		}
	})

	t.Run("download round-trips the bytes", func(t *testing.T) {
		out := filepath.Join(e.Dir, "got.txt")
		e.MustOK(t, "blossom", "download", hash, "-o", out, "--server", media, "--json")
		if got, _ := os.ReadFile(out); string(got) != content {
			t.Errorf("downloaded %q", got)
		}
		if res := e.MustOK(t, "blossom", "download", hash, "-o", "-", "--server", media); res.Stdout != content {
			t.Errorf("-o - wrote %q", res.Stdout)
		}
		resp, err := http.Get(media + "/" + hash + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		if resp.StatusCode != http.StatusOK || body.String() != content {
			t.Errorf("GET /media/<sha>.txt: %d %q", resp.StatusCode, body.String())
		}
	})

	// ohstr/ncli#240: the relay answers list with a plain 404, which the
	// client reports as not_found. Once fixed, expect
	// expectErr(t, "unsupported", ...) here instead of the 404.
	t.Run("list fails: the relay serves no BUD-02 list", func(t *testing.T) {
		for _, server := range []string{srv, media} {
			res := e.Run(t, "blossom", "list", "--identity", alice.Nsec, "--server", server, "--json")
			if res.Code == 0 || res.Stdout != "" || !strings.Contains(res.Stderr, "404") {
				t.Errorf("list on %s should fail with the relay's 404\n%s", server, res)
			}
		}
	})

	t.Run("over maxBlobBytes is invalid_input", func(t *testing.T) {
		big := e.WriteFile("big.bin", strings.Repeat("x", maxBlob+1))
		er := e.Run(t, "blossom", "upload", big, "--identity", alice.Nsec, "--server", srv, "--json").expectErr(t, "invalid_input", true)
		if !strings.Contains(er.Error, "413") {
			t.Errorf("error doesn't carry the relay's 413: %q", er.Error)
		}
	})

	t.Run("a non-member's upload is auth", func(t *testing.T) {
		other := e.WriteFile("eve.txt", "not a member\n")
		er := e.Run(t, "blossom", "upload", other, "--identity", eve.Nsec, "--server", srv, "--json").expectErr(t, "auth", true)
		if !strings.Contains(er.Error, "403") {
			t.Errorf("error doesn't carry the relay's 403: %q", er.Error)
		}
	})

	t.Run("someone else can't delete it", func(t *testing.T) {
		e.Run(t, "blossom", "rm", hash, "--identity", eve.Nsec, "--server", media, "--yes", "--json").expectErr(t, "auth", true)
		if !stored(t, hash) {
			t.Errorf("a refused delete removed the blob")
		}
	})

	t.Run("the uploader deletes it", func(t *testing.T) {
		e.MustOK(t, "blossom", "rm", hash, "--identity", alice.Nsec, "--server", media, "--yes", "--json")
		e.Run(t, "blossom", "download", hash, "--server", media, "--json").ExpectErr(t, "not_found")
		if stored(t, hash) {
			t.Errorf("blob %s is still in the store after rm", hash)
		}
	})
}
