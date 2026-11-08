package relay

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nipB7/server"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/relay/ratelimit"
)

// CountConfig configures POST /count (NIP-45) on the HTTP bridge: the same
// request, NIP-98 and access rules as /query, answered {"count": n}.
type CountConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

// countEnabled reports whether to mount POST /count.
func countEnabled() bool {
	return config.HTTPBridge != nil && config.HTTPBridge.Count != nil && config.HTTPBridge.Count.Enabled
}

// MembershipNoticesConfig configures seat notices: an event the relay signs
// whenever a pubkey gains or loses a seat in a hosted group, readable only
// by that member and the relay's admins. No NIP fixes the kinds; 44100
// (added) and 44101 (removed) are what existing clients read.
//
//	groups:
//	  membershipNotices:
//	    enabled: true
//	    added: 44100
//	    removed: 44101
type MembershipNoticesConfig struct {
	Enabled bool `mapstructure:"enabled"`
	Added   int  `mapstructure:"added"`
	Removed int  `mapstructure:"removed"`
}

// memberNotices returns the seat-notice kinds, defaults filled in, and
// whether notices are on.
func (c *RelayConfig) memberNotices() (relay.MemberNoticeKinds, bool) {
	if c.Groups == nil || c.Groups.MembershipNotices == nil || !c.Groups.MembershipNotices.Enabled {
		return relay.MemberNoticeKinds{}, false
	}
	k := relay.DefaultMemberNoticeKinds
	if n := c.Groups.MembershipNotices; n.Added != 0 {
		k.Added = n.Added
	}
	if n := c.Groups.MembershipNotices; n.Removed != 0 {
		k.Removed = n.Removed
	}
	return k, true
}

// PresenceConfig configures presence snapshots on POST /query: a filter
// {"kinds":[kind],"authors":[...]} is answered with one relay-signed kind
// 20001 per author ("online" while connected here, or its own latest kind
// 20001 status, else "offline"), never stored.
type PresenceConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// Kind is the query kind; 0 means 40902.
	Kind int `mapstructure:"kind"`
}

// presenceKind is the presence query kind, or 0 when presence is off.
func (c *RelayConfig) presenceKind() int {
	if c.Presence == nil || !c.Presence.Enabled {
		return 0
	}
	if c.Presence.Kind != 0 {
		return c.Presence.Kind
	}
	return relay.DefaultPresenceQueryKind
}

// relayAuthoredKinds are the kinds only the relay's key may publish
// because the relay signs them itself: seat notices and the presence query
// kind.
func (c *RelayConfig) relayAuthoredKinds() []int {
	var kinds []int
	if k, ok := c.memberNotices(); ok {
		kinds = append(kinds, k.Added, k.Removed)
	}
	if k := c.presenceKind(); k != 0 {
		kinds = append(kinds, k)
	}
	return kinds
}

// BlossomConfig serves Blossom (BUD-01, BUD-02) on the relay's own origin:
// uploads authorized by kind 24242 naming the relay's host, downloads by
// sha256 with Range, deletes by an uploader or a relay admin.
type BlossomConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// PublicURL is the origin blobs are served from; empty means nip11.url
	// with ws(s) turned into http(s).
	PublicURL string `mapstructure:"publicURL"`
	// UploadPaths accept PUT; empty means [/upload, /media/upload].
	UploadPaths []string `mapstructure:"uploadPaths"`
	// BlobPrefix serves /<prefix>/<sha256>[.ext]; empty means /media.
	BlobPrefix string `mapstructure:"blobPrefix"`
	// MaxBlobBytes caps one blob; 0 means 100 MiB.
	MaxBlobBytes int64 `mapstructure:"maxBlobBytes"`
	// MaxUploadsPerSigner caps one key's uploads in flight; 0 means 2.
	MaxUploadsPerSigner int `mapstructure:"maxUploadsPerSigner"`
	// MaxConcurrentUploads caps all uploads in flight; 0 means 8.
	MaxConcurrentUploads int `mapstructure:"maxConcurrentUploads"`
	// MembersOnly restricts who may upload and download.
	MembersOnly BlossomMembersOnly `mapstructure:"membersOnly"`
	// Storage is where blobs live.
	Storage BlossomStorage `mapstructure:"storage"`
}

// BlossomMembersOnly restricts Blossom to the relay's NIP-43 members (and
// its admins).
type BlossomMembersOnly struct {
	// Upload, unset, follows nip11.limitation.membership_required.
	Upload *bool `mapstructure:"upload"`
	// Download requires a "get" authorization from a member.
	Download bool `mapstructure:"download"`
}

// BlossomStorage picks the blob store.
type BlossomStorage struct {
	// Kind is "dir" (the default) or "s3".
	Kind string `mapstructure:"kind"`
	// Dir holds blobs for kind dir; empty means <store>.blobs.
	Dir string    `mapstructure:"dir"`
	S3  BlossomS3 `mapstructure:"s3"`
}

// BlossomS3 is S3-compatible object storage, such as MinIO. Keep the keys
// out of the file: leave them "" and set NCLI_BLOSSOM_STORAGE_S3_ACCESSKEY
// and NCLI_BLOSSOM_STORAGE_S3_SECRETKEY.
type BlossomS3 struct {
	Endpoint  string `mapstructure:"endpoint"`
	Bucket    string `mapstructure:"bucket"`
	Region    string `mapstructure:"region"`
	Prefix    string `mapstructure:"prefix"`
	AccessKey string `mapstructure:"accessKey"`
	SecretKey string `mapstructure:"secretKey"`
	UseSSL    bool   `mapstructure:"useSSL"`
}

func blossomEnabled() bool {
	return config.Blossom != nil && config.Blossom.Enabled
}

// blossomUploadMembersOnly reports whether only members may upload:
// blossom.membersOnly.upload, or, unset, nip11.limitation.membership_required.
func (c *RelayConfig) blossomUploadMembersOnly() bool {
	if c.Blossom != nil && c.Blossom.MembersOnly.Upload != nil {
		return *c.Blossom.MembersOnly.Upload
	}
	return c.Nip11.Limitation.MembershipRequired
}

// blossomPublicURL is the origin blobs are served from.
func (c *RelayConfig) blossomPublicURL() string {
	if c.Blossom != nil && c.Blossom.PublicURL != "" {
		return strings.TrimSuffix(c.Blossom.PublicURL, "/")
	}
	u, err := url.Parse(c.Nip11.URL)
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	}
	return u.Scheme + "://" + u.Host
}

func (b *BlossomConfig) uploadPaths() []string {
	if len(b.UploadPaths) == 0 {
		return []string{"/upload", "/media/upload"}
	}
	return b.UploadPaths
}

func (b *BlossomConfig) blobPrefix() string {
	if b.BlobPrefix == "" {
		return "/media"
	}
	return strings.TrimSuffix(b.BlobPrefix, "/")
}

// reservedPaths are the relay's own HTTP routes, which Blossom may not
// take.
var reservedPaths = []string{"/query", "/events", "/count", "/_home", "/metrics", "/admin", "/huddle"}

func communityConfigError(format string, args ...any) error {
	return &common.CLIError{Err: fmt.Errorf(format, args...), Code: common.CodeInvalidInput}
}

// checkCommunityConfig validates seat notices, presence and Blossom.
func checkCommunityConfig(c *RelayConfig) error {
	if k, ok := c.memberNotices(); ok {
		if c.Nip11.PrivKey == "" {
			return communityConfigError("groups.membershipNotices needs nip11.privkey: the relay signs each notice")
		}
		if k.Added == k.Removed || k.Added < 0 || k.Removed < 0 {
			return communityConfigError("groups.membershipNotices.added and .removed must be two different kinds")
		}
	}
	if c.presenceKind() != 0 {
		if c.Nip11.PrivKey == "" {
			return communityConfigError("presence needs nip11.privkey: the relay signs each answer")
		}
		if c.HTTPBridge == nil || c.HTTPBridge.Query == nil || !c.HTTPBridge.Query.Enabled {
			return communityConfigError("presence is answered on POST /query: set httpBridge.query.enabled")
		}
	}
	if c.Blossom == nil || !c.Blossom.Enabled {
		return nil
	}
	b := c.Blossom
	if c.blossomPublicURL() == "" {
		return communityConfigError("blossom needs blossom.publicURL or nip11.url: an upload must name the relay's host")
	}
	prefix := b.blobPrefix()
	if !strings.HasPrefix(prefix, "/") || prefix == "/" {
		return communityConfigError("blossom.blobPrefix must be a path such as /media, not %q", b.BlobPrefix)
	}
	for _, p := range append([]string{prefix}, b.uploadPaths()...) {
		if !strings.HasPrefix(p, "/") || p == "/" {
			return communityConfigError("blossom path %q must start with / and name a path", p)
		}
		if slices.ContainsFunc(reservedPaths, func(r string) bool { return p == r || strings.HasPrefix(p, r+"/") }) {
			return communityConfigError("blossom path %q collides with one of the relay's own routes", p)
		}
	}
	switch b.Storage.Kind {
	case "", "dir":
	case "s3":
		if b.Storage.S3.Endpoint == "" || b.Storage.S3.Bucket == "" {
			return communityConfigError("blossom.storage.s3 needs endpoint and bucket")
		}
	default:
		return communityConfigError("blossom.storage.kind must be dir or s3, not %q", b.Storage.Kind)
	}
	return nil
}

// newBlobStore opens the configured blob store.
func newBlobStore(c *RelayConfig) (server.Store, error) {
	st := c.Blossom.Storage
	if st.Kind == "s3" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.NewS3Store(ctx, server.S3Options{
			Endpoint:  st.S3.Endpoint,
			Bucket:    st.S3.Bucket,
			Region:    st.S3.Region,
			Prefix:    st.S3.Prefix,
			AccessKey: st.S3.AccessKey,
			SecretKey: st.S3.SecretKey,
			UseSSL:    st.S3.UseSSL,
		})
	}
	dir := st.Dir
	if dir == "" {
		dir = filepath.Clean(c.RelayNotesDb) + ".blobs"
	}
	return server.NewDirStore(dir)
}

// relayAccess answers who is an admin (the relay key, nip86.admins) and who
// is a member (a NIP-43 member or an admin); a banned key is neither.
// membership may be set after the relayAccess is built, as long as it is
// before the relay serves its first request.
type relayAccess struct {
	admins     []string
	membership *relay.MembershipService
	moderation *relayModeration
}

func newRelayAccess(membership *relay.MembershipService, moderation *relayModeration) *relayAccess {
	return &relayAccess{admins: nip86Admins(), membership: membership, moderation: moderation}
}

func (a *relayAccess) isAdmin(pk string) bool {
	return slices.ContainsFunc(a.admins, func(admin string) bool { return strings.EqualFold(admin, pk) })
}

func (a *relayAccess) isMember(pk string) bool {
	if a.moderation.bannedPubkey(pk) {
		return false
	}
	return a.isAdmin(pk) || (a.membership != nil && a.membership.IsMember(pk))
}

// newBlossomServer builds the Blossom server for c. Members are NIP-43
// members; admins (the relay key, nip86.admins) count as members and may
// delete any blob; a banned key may do neither. limiter, when set, counts
// each request against limits.blossom.
func newBlossomServer(c *RelayConfig, access *relayAccess, limiter *ratelimit.Limiter) (*server.Server, error) {
	store, err := newBlobStore(c)
	if err != nil {
		return nil, err
	}
	member := access.isMember
	anyone := func(pk string) bool { return !access.moderation.bannedPubkey(pk) }

	b := c.Blossom
	mayUpload := anyone
	if c.blossomUploadMembersOnly() {
		mayUpload = member
	}
	var mayDownload func(string) bool
	if b.MembersOnly.Download {
		mayDownload = member
	}
	var rateLimit func(*http.Request, bool, string) (time.Duration, bool)
	if limiter != nil {
		rateLimit = limiter.AllowBlossom
	}
	return server.New(server.Options{
		Store:               store,
		PublicURL:           c.blossomPublicURL(),
		UploadPaths:         b.uploadPaths(),
		BlobPrefix:          b.blobPrefix(),
		MayUpload:           mayUpload,
		MayDownload:         mayDownload,
		IsAdmin:             access.isAdmin,
		MaxBlobBytes:        b.MaxBlobBytes,
		MaxUploadsPerSigner: b.MaxUploadsPerSigner,
		MaxUploads:          b.MaxConcurrentUploads,
		RateLimit:           rateLimit,
	})
}
