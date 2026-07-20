package meilisearch

import (
	"context"
	"testing"
	"time"

	"github.com/ohstr/nmilat/search"
)

// To run this test, ensure you have a local Meilisearch instance running:
// docker run -d -p 7700:7700 getmeili/meilisearch:v1.10.0
// Then run: go test -v -run TestMeiliClient_FindProfiles_SortedByScore ./meilisearch
func TestMeiliClient_FindProfiles_SortedByScore(t *testing.T) {
	host := "http://localhost:7700"
	apiKey := ""
	indexName := "test_profiles_score_sort"

	client := NewMeiliClient(host, apiKey, indexName)

	ctx := context.Background()
	err := client.Initialize(ctx)
	if err != nil {
		t.Skipf("Skipping integration test; could not initialize Meilisearch client (is it running?): %v", err)
	}

	time.Sleep(1 * time.Second) // wait for index creation

	// Test case:
	// pubkey1 matches "loki" perfectly but has low score.
	// pubkey2 has an extra word but has a high score.
	// By default, Exactness ranking rule wins against Score.
	// We pass `Sort: []string{"score:desc"}` in the SDK to ensure Score prevails.
	docs := []*search.ProfileDocument{
		{
			ID:          "pubkey1",
			Name:        "loki",
			DisplayName: "loki",
			Score:       0,
		},
		{
			ID:          "pubkey2",
			Name:        "loki nakamoto",
			DisplayName: "loki Nakamoto",
			Score:       1000,
		},
	}

	err = client.BulkIndex(docs)
	if err != nil {
		t.Fatalf("Failed to index: %v", err)
	}
	time.Sleep(2 * time.Second) // wait for indexing to finish

	results, err := client.FindProfiles(ctx, "loki", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	if results[0] != "pubkey2" {
		t.Errorf("Expected pubkey2 (highest score) to be first, but got: %v", results)
	}

	if results[1] != "pubkey1" {
		t.Errorf("Expected pubkey1 to be second, but got: %v", results)
	}
}
