// Package meilisearch wraps the Meilisearch HTTP API for indexing and
// querying events, shared by the relay and reindex commands.
package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	meilisearch "github.com/meilisearch/meilisearch-go"
	"github.com/ohstr/nmilat/config"
	"github.com/ohstr/nmilat/search"
	"github.com/ohstr/nmilat/search/ranking"
	"github.com/ohstr/nmilat/nip19"
)

type MeiliClient struct {
	client    meilisearch.ServiceManager
	indexName string
	apiKey    string
	host      string
}

func NewMeiliClient(host, apiKey, indexName string) *MeiliClient {
	return &MeiliClient{
		host:      host,
		apiKey:    apiKey,
		indexName: indexName,
	}
}

func (m *MeiliClient) Initialize(ctx context.Context) error {
	// Initialize client using the public New method which returns a ServiceManager
	m.client = meilisearch.New(m.host, meilisearch.WithAPIKey(m.apiKey))

	// Ensure index exists
	_, err := m.client.GetIndex(m.indexName)
	if err != nil {
		// Create index if it doesn't exist
		_, err = m.client.CreateIndex(&meilisearch.IndexConfig{
			Uid:        m.indexName,
			PrimaryKey: "id",
		})
		if err != nil {
			return err
		}

		// Wait for task to complete?
		// For simplicity in R1, we proceed. Meilisearch handles subsequent updates queueing.
	}

	// Manual settings update to avoid SDK sending deprecated fields (disableOnNumbers)
	// which causes errors with Meilisearch v1.10+
	settingsMap := map[string]interface{}{
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
		"searchableAttributes": []string{
			"id",
			"name",
			"display_name",
			"about",
			"nip05",
			"lud16",
			"npub",
		},
		"filterableAttributes": []string{
			"pubkey",
			"kind",
			"is_human",
		},
		"sortableAttributes": []string{
			"created_at",
			"promoted",
			"bot_score",
			"score",
		},
		"typoTolerance": map[string]interface{}{
			"enabled": true,
			"minWordSizeForTypos": map[string]interface{}{
				"oneTypo":  config.Get().Search.Engine.TypoOneMinWordSize,
				"twoTypos": config.Get().Search.Engine.TypoTwoMinWordSize,
			},
		},
		"pagination": map[string]interface{}{
			"maxTotalHits": config.Get().Search.Engine.MaxTotalHits,
		},
	}

	body, err := json.Marshal(settingsMap)
	if err != nil {
		return err
	}

	url := m.host + "/indexes/" + m.indexName + "/settings"
	req, err := http.NewRequest("PATCH", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to update settings: %s, body: %s", resp.Status, string(respBody))
	}

	return nil
}

func (m *MeiliClient) DeleteIndex(ctx context.Context) error {
	_, err := m.client.Index(m.indexName).DeleteAllDocuments(&meilisearch.DocumentOptions{})
	return err
}

func (m *MeiliClient) Shutdown(ctx context.Context) error {
	// Meilisearch client doesn't need explicit shutdown, but we implement it for the interface
	return nil
}

func (m *MeiliClient) IndexProfileWithMetrics(ctx context.Context, doc *search.ProfileDocument, getMetricsFunc func(pubkey string) (int64, error)) error {
	if getMetricsFunc != nil {
		if extraMetrics, err := getMetricsFunc(doc.ID); err == nil {
			doc.Score += extraMetrics
		}
	}
	return m.IndexProfile(ctx, doc)
}

func (m *MeiliClient) IndexProfile(ctx context.Context, doc *search.ProfileDocument) error {
	// This method is part of the search.Indexer interface, but the service layer
	// is designed to call BulkIndex. For now, this can be a no-op or
	// call BulkIndex with a single document.
	// Given the instruction to rewrite, and the presence of BulkIndex,
	// we'll assume the service layer will use BulkIndex.
	// If this method is still called, it should probably be implemented.
	// For now, let's make it call BulkIndex for consistency.
	return m.BulkIndex([]*search.ProfileDocument{doc})
}

func (m *MeiliClient) BulkIndex(docs []*search.ProfileDocument) error {
	if len(docs) == 0 {
		return nil
	}
	_, err := m.client.Index(m.indexName).AddDocuments(docs, nil)
	return err
}

func (m *MeiliClient) FindProfiles(ctx context.Context, query string, limit int) ([]string, error) {
	searchReq := &meilisearch.SearchRequest{
		Limit:                int64(limit),
		AttributesToRetrieve: []string{"id"}, // optimize retrieval: 'id' is the pubkey
		Sort:                 []string{"score:desc"},
	}

	searchQuery := query

	// 1. Strict Hex Pubkey Check
	if len(query) == 64 && nip19.CheckPublicKey(query) == nil {
		searchReq.AttributesToSearchOn = []string{"id"}
	} else if strings.HasPrefix(query, "npub1") {
		// 2. Strict Npub Check
		_, dec, err := nip19.Decode(query)
		if err == nil {
			if hexStr, ok := dec.(string); ok && len(hexStr) == 64 {
				searchReq.AttributesToSearchOn = []string{"id"}
				searchQuery = hexStr
			}
		}
	} else if strings.HasPrefix(query, "nprofile1") {
		// 3. Strict Nprofile Check
		hexStr, err := nip19.DecodeNprofile(query)
		if err == nil && len(hexStr) == 64 {
			searchReq.AttributesToSearchOn = []string{"id"}
			searchQuery = hexStr
		}
	} else if strings.Contains(query, "@") && (ranking.IsValidNip05Format(query) || ranking.IsValidLud16Format(query)) {
		// 4. Strict NIP-05 / LUD-16 check
		searchReq.AttributesToSearchOn = []string{"nip05", "lud16"}
	}

	resp, err := m.client.Index(m.indexName).Search(searchQuery, searchReq)
	if err != nil {
		return nil, err
	}

	pubkeys := make([]string, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		// hits are type meilisearch.Hit which is map[string]interface{}
		// We use a robust extraction
		var id string

		// Convert to JSON and back to map for maximum compatibility with SDK types
		b, err := json.Marshal(hit)
		if err != nil {
			continue
		}
		var mm map[string]interface{}
		if err := json.Unmarshal(b, &mm); err == nil {
			if pk, ok := mm["id"].(string); ok {
				id = pk
			}
		}

		if id != "" {
			pubkeys = append(pubkeys, id)
		}
	}
	return pubkeys, nil
}

func (mc *MeiliClient) DeleteProfile(ctx context.Context, pubkey string) error {
	_, err := mc.client.Index(mc.indexName).DeleteDocument(pubkey, nil)
	return err
}

func (m *MeiliClient) UpdateScore(ctx context.Context, pubkey string, score int64) error {
	updateDoc := map[string]interface{}{
		"id":    pubkey,
		"score": score,
	}
	_, err := m.client.Index(m.indexName).UpdateDocuments([]map[string]interface{}{updateDoc}, nil)
	return err
}
