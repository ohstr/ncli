// Package meilisearch wraps the Meilisearch HTTP API for indexing and
// querying events, shared by the relay and reindex commands.
package meilisearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	meilisearch "github.com/meilisearch/meilisearch-go"
	"github.com/ohstr/nmilat/config"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/search"
	"github.com/ohstr/nmilat/utils"
)

// DefaultMaxPayload caps one document request, below Meilisearch's 100 MB
// default payload limit.
const DefaultMaxPayload = 50 << 20

type MeiliClient struct {
	client     meilisearch.ServiceManager
	indexName  string
	apiKey     string
	host       string
	maxPayload int
	noAbout    bool
	http       *http.Client
	last       *atomic.Pointer[TaskStat]
}

// Option configures a MeiliClient.
type Option func(*MeiliClient)

// WithoutAbout leaves a profile's about text out of what is searched.
func WithoutAbout() Option { return func(m *MeiliClient) { m.noAbout = true } }

// WithMaxPayload caps one document request at n bytes of JSON.
func WithMaxPayload(n int) Option {
	return func(m *MeiliClient) {
		if n > 0 {
			m.maxPayload = n
		}
	}
}

func NewMeiliClient(host, apiKey, indexName string, opts ...Option) *MeiliClient {
	m := &MeiliClient{
		client:     meilisearch.New(host, meilisearch.WithAPIKey(apiKey)),
		host:       host,
		apiKey:     apiKey,
		indexName:  indexName,
		maxPayload: DefaultMaxPayload,
		http:       &http.Client{Timeout: 30 * time.Second},
		last:       &atomic.Pointer[TaskStat]{},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Settings are the index settings this client gives an index it creates.
// id and npub aren't searchable: their random words would make every
// write recompute prefixes shared by millions of documents. A pubkey
// query fetches its document by id instead (FindProfiles).
func (m *MeiliClient) Settings() map[string]any {
	searchable := []string{"name", "display_name", "about", "nip05", "lud16"}
	if m.noAbout {
		searchable = []string{"name", "display_name", "nip05", "lud16"}
	}
	return map[string]any{
		"rankingRules": []string{
			"words",
			"typo",
			"proximity",
			"attribute",
			"sort",
			"exactness",
			"score:desc",
			"promoted:desc",
			"nip05:desc",
			"lud16:desc",
			"is_human:desc",
			"bot_score:asc",
			"created_at:desc",
		},
		"searchableAttributes": searchable,
		"filterableAttributes": []any{map[string]any{
			"attributePatterns": []string{"pubkey", "kind", "is_human"},
			"features": map[string]any{
				"facetSearch": false,
				"filter":      map[string]any{"equality": true, "comparison": false},
			},
		}},
		"sortableAttributes": []string{"created_at", "promoted", "bot_score", "score"},
		"facetSearch":        false,
		"typoTolerance": map[string]any{
			"enabled": true,
			"minWordSizeForTypos": map[string]any{
				"oneTypo":  config.Get().Search.Engine.TypoOneMinWordSize,
				"twoTypos": config.Get().Search.Engine.TypoTwoMinWordSize,
			},
		},
		"pagination": map[string]any{
			"maxTotalHits": config.Get().Search.Engine.MaxTotalHits,
		},
	}
}

// SettingsFingerprint identifies Settings: an index built with other
// settings is rebuilt, never changed in place.
func (m *MeiliClient) SettingsFingerprint() string {
	raw, _ := json.Marshal(m.Settings()) // map keys marshal sorted
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// Initialize creates the index with Settings when it doesn't exist. An
// existing index keeps its settings: changing them makes Meilisearch
// reindex every document in place, so a relay rebuilds into a new index
// instead.
func (m *MeiliClient) Initialize(ctx context.Context) error {
	if _, err := m.client.GetIndexWithContext(ctx, m.indexName); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "index_not_found") {
		return err
	}
	info, err := m.client.CreateIndexWithContext(ctx, &meilisearch.IndexConfig{Uid: m.indexName, PrimaryKey: "id"})
	if err != nil {
		return err
	}
	if err := m.wait(ctx, info.TaskUID); err != nil && !strings.Contains(err.Error(), "index_already_exists") {
		return err
	}
	return m.applySettings(ctx)
}

// applySettings sends Settings and waits for Meilisearch to apply them.
// The settings go as raw JSON: the SDK's struct sends fields some
// Meilisearch versions reject.
func (m *MeiliClient) applySettings(ctx context.Context) error {
	body, err := json.Marshal(m.Settings())
	if err != nil {
		return err
	}
	var info struct {
		TaskUID int64 `json:"taskUid"`
	}
	if err := m.do(ctx, http.MethodPatch, "/indexes/"+url.PathEscape(m.indexName)+"/settings", body, &info); err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	return m.wait(ctx, info.TaskUID)
}

// do sends one request to Meilisearch and decodes its JSON answer into out.
func (m *MeiliClient) do(ctx context.Context, method, path string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.host+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, raw)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// CountDocuments returns how many documents the index holds.
func (m *MeiliClient) CountDocuments(ctx context.Context) (int64, error) {
	stats, err := m.client.Index(m.indexName).GetStatsWithContext(ctx)
	if err != nil {
		return 0, err
	}
	return stats.NumberOfDocuments, nil
}

// IndexName returns the index this client writes to.
func (m *MeiliClient) IndexName() string { return m.indexName }

// SwapWith exchanges this index's contents with other's in one Meilisearch
// task, after every task queued before it, and waits for it.
func (m *MeiliClient) SwapWith(ctx context.Context, other string) error {
	info, err := m.client.SwapIndexesWithContext(ctx, []*meilisearch.SwapIndexesParams{{Indexes: []string{m.indexName, other}}})
	if err != nil {
		return err
	}
	return m.wait(ctx, info.TaskUID)
}

// RemoveIndex deletes the index itself, settings included, and waits for
// it. A missing index is not an error.
func (m *MeiliClient) RemoveIndex(ctx context.Context) error {
	info, err := m.client.DeleteIndexWithContext(ctx, m.indexName)
	if err != nil {
		return err
	}
	err = m.wait(ctx, info.TaskUID)
	if err != nil && strings.Contains(err.Error(), "index_not_found") {
		return nil
	}
	return err
}

func (m *MeiliClient) wait(ctx context.Context, taskUID int64) error {
	task, err := m.client.WaitForTaskWithContext(ctx, taskUID, 100*time.Millisecond)
	if err != nil {
		return err
	}
	if task.Status == meilisearch.TaskStatusFailed {
		return fmt.Errorf("meilisearch task %d failed: %s: %s", taskUID, task.Error.Code, task.Error.Message)
	}
	if task.Status == meilisearch.TaskStatusCanceled {
		return fmt.Errorf("meilisearch task %d was canceled", taskUID)
	}
	return nil
}

// tasks is changes enqueued for one call: Wait waits for each, in order
// (search.Pending).
type tasks struct {
	m    *MeiliClient
	uids []int64
	docs []int
}

func (t *tasks) Wait(ctx context.Context) error {
	for i, uid := range t.uids {
		if err := t.m.wait(ctx, uid); err != nil {
			return err
		}
		if t.docs != nil {
			t.m.observe(ctx, uid, t.docs[i])
		}
	}
	return nil
}

// Chunks splits encoded documents into JSON arrays of at most max bytes
// each; a document larger than max goes alone.
func Chunks(docs []json.RawMessage, max int) [][]json.RawMessage {
	var out [][]json.RawMessage
	start, size := 0, 2
	for i, d := range docs {
		if i > start && size+len(d)+1 > max {
			out = append(out, docs[start:i])
			start, size = i, 2
		}
		size += len(d) + 1
	}
	if start < len(docs) {
		out = append(out, docs[start:])
	}
	return out
}

// EnqueueUpsert adds or replaces documents, in as many requests as the
// payload cap needs, without waiting for Meilisearch to apply them
// (search.Enqueuer).
func (m *MeiliClient) EnqueueUpsert(ctx context.Context, docs []*search.ProfileDocument) (search.Pending, error) {
	t := &tasks{m: m}
	if len(docs) == 0 {
		return t, nil
	}
	enc := make([]json.RawMessage, len(docs))
	for i, d := range docs {
		raw, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		enc[i] = raw
	}
	pk := "id"
	for _, chunk := range Chunks(enc, m.maxPayload) {
		info, err := m.client.Index(m.indexName).AddDocumentsWithContext(ctx, chunk, &meilisearch.DocumentOptions{PrimaryKey: &pk})
		if err != nil {
			return nil, err
		}
		t.uids = append(t.uids, info.TaskUID)
		t.docs = append(t.docs, len(chunk))
	}
	return t, nil
}

// EnqueueDelete removes documents by pubkey without waiting
// (search.Enqueuer).
func (m *MeiliClient) EnqueueDelete(ctx context.Context, pubkeys []string) (search.Pending, error) {
	t := &tasks{m: m}
	if len(pubkeys) == 0 {
		return t, nil
	}
	info, err := m.client.Index(m.indexName).DeleteDocumentsWithContext(ctx, pubkeys, nil)
	if err != nil {
		return nil, err
	}
	t.uids = []int64{info.TaskUID}
	return t, nil
}

// Upsert adds or replaces documents and returns once Meilisearch applied
// them (search.Writer).
func (m *MeiliClient) Upsert(ctx context.Context, docs []*search.ProfileDocument) error {
	p, err := m.EnqueueUpsert(ctx, docs)
	if err != nil {
		return err
	}
	return p.Wait(ctx)
}

// Delete removes documents by pubkey and returns once Meilisearch applied
// it (search.Writer).
func (m *MeiliClient) Delete(ctx context.Context, pubkeys []string) error {
	p, err := m.EnqueueDelete(ctx, pubkeys)
	if err != nil {
		return err
	}
	return p.Wait(ctx)
}

// TaskStat is how long Meilisearch took over the batch that applied the
// last document task, and its slowest step.
type TaskStat struct {
	At          time.Time     `json:"at"`
	Task        int64         `json:"task"`
	Documents   int           `json:"documents"`
	Duration    time.Duration `json:"-"`
	Batch       int64         `json:"batch"`
	BatchDocs   int64         `json:"batch_documents"`
	SlowestStep string        `json:"slowest_step,omitempty"`
	SlowestTime time.Duration `json:"-"`
	DurationMS  int64         `json:"duration_ms"`
	SlowestMS   int64         `json:"slowest_ms,omitempty"`
}

// LastTask is the last document task this client (and the clients made
// with ForIndex) saw applied; nil before the first.
func (m *MeiliClient) LastTask() *TaskStat { return m.last.Load() }

// ForIndex is a client for another index on the same server, sharing its
// options and LastTask.
func (m *MeiliClient) ForIndex(name string) *MeiliClient {
	c := *m
	c.indexName = name
	c.client = meilisearch.New(m.host, meilisearch.WithAPIKey(m.apiKey))
	return &c
}

// observe records the batch that applied task uid. It is best effort:
// a failure leaves the last value.
func (m *MeiliClient) observe(ctx context.Context, uid int64, docs int) {
	var task struct {
		BatchUID *int64 `json:"batchUid"`
		Duration string `json:"duration"`
	}
	if m.do(ctx, http.MethodGet, fmt.Sprintf("/tasks/%d", uid), nil, &task) != nil || task.BatchUID == nil {
		return
	}
	st := &TaskStat{At: time.Now(), Task: uid, Documents: docs, Batch: *task.BatchUID}
	st.Duration, _ = ParseISODuration(task.Duration)
	var batch batchInfo
	if m.do(ctx, http.MethodGet, fmt.Sprintf("/batches/%d", *task.BatchUID), nil, &batch) == nil {
		if d, err := ParseISODuration(batch.Duration); err == nil {
			st.Duration = d
		}
		st.BatchDocs = batch.Details.ReceivedDocuments
		st.SlowestStep, st.SlowestTime = SlowestStep(batch.Stats.ProgressTrace)
	}
	st.DurationMS, st.SlowestMS = st.Duration.Milliseconds(), st.SlowestTime.Milliseconds()
	m.last.Store(st)
}

type batchInfo struct {
	Duration string `json:"duration"`
	Details  struct {
		ReceivedDocuments int64 `json:"receivedDocuments"`
	} `json:"details"`
	Stats struct {
		ProgressTrace map[string]string `json:"progressTrace"`
	} `json:"stats"`
}

// SlowestStep is the longest leaf step of a batch's progressTrace, whose
// keys are steps nested with " > " and values Go-style durations
// ("1.23s"). A parent step's time includes its children's, so only steps
// with no child count.
func SlowestStep(trace map[string]string) (string, time.Duration) {
	keys := make([]string, 0, len(trace))
	for k := range trace {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var best string
	var most time.Duration
	for i, k := range keys {
		if i+1 < len(keys) && strings.HasPrefix(keys[i+1], k+" > ") {
			continue
		}
		d, err := time.ParseDuration(strings.ReplaceAll(trace[k], "µs", "us"))
		if err != nil || d <= most {
			continue
		}
		best, most = k, d
	}
	return best, most
}

// ParseISODuration reads the ISO 8601 durations Meilisearch reports
// ("PT1.5S", "PT2M3.1S", "PT1H").
func ParseISODuration(s string) (time.Duration, error) {
	rest, ok := strings.CutPrefix(s, "PT")
	if !ok || rest == "" {
		return 0, fmt.Errorf("not an ISO 8601 duration: %q", s)
	}
	var total time.Duration
	for rest != "" {
		i := strings.IndexAny(rest, "HMS")
		if i <= 0 {
			return 0, fmt.Errorf("not an ISO 8601 duration: %q", s)
		}
		unit := map[byte]string{'H': "h", 'M': "m", 'S': "s"}[rest[i]]
		d, err := time.ParseDuration(rest[:i] + unit)
		if err != nil {
			return 0, err
		}
		total += d
		rest = rest[i+1:]
	}
	return total, nil
}

// Identity names this incarnation of the index: its uid and when it was
// created, so a relay notices it was deleted or recreated behind its back.
// It is "" when the index doesn't exist.
func (m *MeiliClient) Identity(ctx context.Context) (string, error) {
	idx, err := m.client.GetIndexWithContext(ctx, m.indexName)
	if err != nil {
		if strings.Contains(err.Error(), "index_not_found") {
			return "", nil
		}
		return "", err
	}
	return idx.UID + "@" + idx.CreatedAt.UTC().Format(time.RFC3339Nano), nil
}

// PubkeyQuery returns the pubkey a query names when it is a single npub,
// nprofile or 64-character hex token.
func PubkeyQuery(query string) (string, bool) {
	q := strings.TrimSpace(query)
	if q == "" || strings.ContainsAny(q, " \t\r\n") {
		return "", false
	}
	switch {
	case len(q) == 64:
		q = strings.ToLower(q)
		if nip19.CheckPublicKey(q) == nil {
			return q, true
		}
	case strings.HasPrefix(q, "npub1"):
		if _, dec, err := nip19.Decode(q); err == nil {
			if pk, ok := dec.(string); ok && len(pk) == 64 {
				return pk, true
			}
		}
	case strings.HasPrefix(q, "nprofile1"):
		if pk, err := nip19.DecodeNprofile(q); err == nil && len(pk) == 64 {
			return pk, true
		}
	}
	return "", false
}

func (m *MeiliClient) FindProfiles(ctx context.Context, query string, limit int) ([]string, error) {
	if pk, ok := PubkeyQuery(query); ok {
		return m.findPubkey(ctx, pk)
	}

	searchReq := &meilisearch.SearchRequest{
		Limit:                int64(limit),
		AttributesToRetrieve: []string{"id"}, // optimize retrieval: 'id' is the pubkey
		Sort:                 []string{"score:desc"},
	}
	// nip-05 or lud16 address: search only those fields.
	if _, _, ok := utils.ParseIdentifier(query); ok {
		searchReq.AttributesToSearchOn = []string{"nip05", "lud16"}
	}

	resp, err := m.client.Index(m.indexName).SearchWithContext(ctx, query, searchReq)
	if err != nil {
		return nil, err
	}

	pubkeys := make([]string, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		var id string
		if raw, ok := hit["id"]; ok && json.Unmarshal(raw, &id) == nil && id != "" {
			pubkeys = append(pubkeys, id)
		}
	}
	return pubkeys, nil
}

// findPubkey fetches the one profile a pubkey query names, by its id.
func (m *MeiliClient) findPubkey(ctx context.Context, pk string) ([]string, error) {
	var doc struct {
		ID string `json:"id"`
	}
	err := m.client.Index(m.indexName).GetDocumentWithContext(ctx, pk, &meilisearch.DocumentQuery{Fields: []string{"id"}}, &doc)
	var me *meilisearch.Error
	if errors.As(err, &me) && me.StatusCode == http.StatusNotFound {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return []string{doc.ID}, nil
}
