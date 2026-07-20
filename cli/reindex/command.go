// Package reindex is the reindexing engine behind the running relay's
// `/admin/reindex/{search,zaps}` HTTP handlers (cli/relay/service.go,
// triggered via `ncli relay reindex`). It has no CLI command of its
// own -- reindexing only happens through a live relay now.
package reindex

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ohstr/ncli/cli/common/meilisearch"
	"github.com/ohstr/nmilat/search"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog/log"
)

type Config struct {
	RelayNotesDb string `mapstructure:"store"`
	Search       struct {
		Host      string `mapstructure:"host"`
		Key       string `mapstructure:"key"`
		IndexName string `mapstructure:"index_name"`
	} `mapstructure:"search"`
}

// ExecuteSearchReindex is called as a background goroutine from the running
// relay's `/admin/reindex/search` HTTP handler (cli/relay/service.go) -- it
// must never call log.Fatal/os.Exit, which would take the whole relay
// process down over a single failed reindex request.
func ExecuteSearchReindex(cfg *Config, store *relay.EventStore) error {
	if !SearchState.TryStart() {
		log.Warn().Msg("search reindexing already in progress, ignoring duplicate request")
		return nil
	}
	defer SearchState.Complete()

	log.Info().Msg("connecting to search backend...")
	searchClient := meilisearch.NewMeiliClient(
		cfg.Search.Host,
		cfg.Search.Key,
		cfg.Search.IndexName,
	)
	if err := searchClient.Initialize(context.Background()); err != nil {
		return fmt.Errorf("failed to init search backend: %w", err)
	}

	log.Info().Msg("starting search reindexing...")
	start := time.Now()

	ctx := context.Background()
	f := nip01.NewSubscriptionFilterGroup()
	filter := nip01.SubscriptionFilter{Kinds: []int{0}}
	f.Add(&filter)

	q, err := relay.NewStoreQuery(store, f)
	if err != nil {
		return fmt.Errorf("failed to create query: %w", err)
	}

	potEventsCh := make(chan *relay.PotentialEvent)
	var wg sync.WaitGroup
	consumerDone := make(chan struct{})

	// Initialize VerificationWorker for re-verification if needed
	vWorker := relay.NewProfileVerificationWorker(store, searchClient)
	vWorker.Start(20) // Use fewer goroutines for reindexing to avoid hitting rate limits too fast
	defer vWorker.Stop()

	// Consumer goroutine
	go func() {
		defer close(consumerDone)
		var batch []*search.ProfileDocument
		count := 0
		for pe := range potEventsCh {
			event, err := store.FindEvent(pe.Evsid)
			if err != nil {
				wg.Done()
				continue
			}

			if doc := search.FromEvent(event); doc != nil {
				// Inject BoltDB profile metrics into the document
				if metrics, err := store.GetProfileMetrics(event.PubKey); err == nil && metrics != nil {
					doc.Score += metrics.TotalScore() - metrics.BaseScore // BaseScore is newly calculated by FromEvent
				}

				// Trigger (re)verification. Migration ensures LUD-16 is re-verified,
				// but we respect cache for other fields unless they are expired.
				vWorker.QueueJob(event)

				batch = append(batch, doc)
				count++
				SearchState.AddProgress(1)

				if len(batch) >= 500 {
					if err := searchClient.BulkIndex(batch); err != nil {
						log.Error().Err(err).Msg("batch indexing error")
					} else {
						log.Info().Int("indexed", count).Msg("progress...")
					}
					batch = nil
				}
			}
			wg.Done()
		}
		// Flush final batch
		if len(batch) > 0 {
			if err := searchClient.BulkIndex(batch); err != nil {
				log.Error().Err(err).Msg("failed to bulk index final batch")
			} else {
				log.Info().Int("indexed", count).Msg("final batch indexed")
			}
		}

		// Wait for verification jobs to finish if we want precise scores in the index immediately.
		// However, VerificationWorker updates the search backend asynchronously anyway via searchService.UpdateScore.
		// For a clean reindex, we might want to wait a bit or just let it finish in background.
		log.Info().Msg("waiting for verification worker to finish remaining jobs...")
		// We can't easily wait for a specific number of jobs without extending the worker,
		// but since we called Stop() in defer, it will wait for the channel to empty.
		// So we just log.

		log.Info().Int("total", count).Str("duration", time.Since(start).String()).Msg("search reindexing complete")
	}()

	// Producer
	log.Info().Msg("fetching events...")
	if err := q.Fetch(ctx, potEventsCh, &wg, false); err != nil {
		return fmt.Errorf("failed to execute query: %w", err)
	}

	wg.Wait()
	close(potEventsCh)
	<-consumerDone
	return nil
}

// ExecuteZapReindex is called as a background goroutine from the running
// relay's `/admin/reindex/zaps` HTTP handler (cli/relay/service.go) -- see
// ExecuteSearchReindex for why it must never call log.Fatal/os.Exit.
func ExecuteZapReindex(store *relay.EventStore) error {
	if !ZapsState.TryStart() {
		log.Warn().Msg("zap reindexing already in progress, ignoring duplicate request")
		return nil
	}
	defer ZapsState.Complete()

	log.Info().Msg("starting zap reindexing...")
	start := time.Now()
	zapCount, err := store.ReindexZaps(context.Background(), func(c int) {
		ZapsState.AddProgress(0) // Logic actually relies on internal Zaps processing count anyway.
		if c%1000 == 0 {
			ZapsState.mu.Lock()
			ZapsState.TotalProcessed = c // Overwrite total accurately
			ZapsState.mu.Unlock()
			log.Info().Int("zaps_indexed", c).Msg("progress...")
		}
	})
	if err != nil {
		return fmt.Errorf("failed to reindex zaps: %w", err)
	}
	log.Info().Int("total_zaps", zapCount).Str("duration", time.Since(start).String()).Msg("zap reindexing complete")
	return nil
}
