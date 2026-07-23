package store

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

func TestLeaseIsExclusive(t *testing.T) {
	databaseURL := os.Getenv("DOCWEAVE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DOCWEAVE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Pool.Close()
	if err := Migrate(ctx, database.Pool); err != nil {
		t.Fatal(err)
	}
	crawlID, err := database.CreateCrawl(ctx, []string{"https://example.com/docs"}, []string{"example.com"}, 1, 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = database.Pool.Exec(ctx, "DELETE FROM crawls WHERE id=$1", crawlID) })

	var group sync.WaitGroup
	results := make(chan *Work, 2)
	for _, workerID := range []string{"worker-a", "worker-b"} {
		group.Add(1)
		go func() {
			defer group.Done()
			work, leaseErr := database.Lease(ctx, workerID, time.Minute)
			if leaseErr != nil {
				t.Error(leaseErr)
			}
			results <- work
		}()
	}
	group.Wait()
	close(results)
	leased := 0
	for work := range results {
		if work != nil {
			leased++
		}
	}
	if leased != 1 {
		t.Fatalf("expected exactly one worker to lease the URL, got %d", leased)
	}
}

func TestExpiredLeaseIsRecovered(t *testing.T) {
	databaseURL := os.Getenv("DOCWEAVE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DOCWEAVE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Pool.Close()
	if err := Migrate(ctx, database.Pool); err != nil {
		t.Fatal(err)
	}
	crawlID, err := database.CreateCrawl(ctx, []string{"https://example.com/recovery"}, []string{"example.com"}, 1, 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = database.Pool.Exec(ctx, "DELETE FROM crawls WHERE id=$1", crawlID) })

	first, err := database.Lease(ctx, "worker-that-crashes", 20*time.Millisecond)
	if err != nil || first == nil {
		t.Fatalf("first lease: work=%v err=%v", first, err)
	}
	time.Sleep(40 * time.Millisecond)
	recovered, err := database.Lease(ctx, "replacement-worker", time.Minute)
	if err != nil || recovered == nil {
		t.Fatalf("recovered lease: work=%v err=%v", recovered, err)
	}
	if recovered.ID != first.ID || recovered.Attempts != 2 {
		t.Fatalf("unexpected recovery: first=%#v recovered=%#v", first, recovered)
	}
}
