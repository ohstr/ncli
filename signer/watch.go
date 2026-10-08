package signer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"time"
)

// Watch polls the policy file and its authors files every interval and
// reloads when any content hash changes -- for mounts (Kubernetes
// ConfigMaps/Secrets) that swap files without a SIGHUP. A bad file keeps
// the active policy; its error is logged once per distinct content.
func (s *Server) Watch(ctx context.Context, interval time.Duration) {
	last := hashFiles(s.Policy().Files)
	failed := ""
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cur := hashFiles(s.Policy().Files)
		if cur == last || cur == failed {
			continue
		}
		if err := s.Reload(); err != nil {
			failed = cur
			continue
		}
		failed = ""
		// The new policy may name different authors files.
		last = hashFiles(s.Policy().Files)
	}
}

// hashFiles digests each file's content, following symlinks. A missing
// file hashes as "missing".
func hashFiles(paths []string) string {
	parts := make([]string, len(paths))
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			parts[i] = p + "=missing"
			continue
		}
		sum := sha256.Sum256(data)
		parts[i] = p + "=" + hex.EncodeToString(sum[:])
	}
	return strings.Join(parts, "\n")
}
