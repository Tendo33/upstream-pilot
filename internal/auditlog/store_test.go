package auditlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStorePaginationAndOwnerIsolation(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.July, 25, 10, 0, 0, 0, time.UTC)
	records := []Record{
		{ID: "a-1", OwnerID: "owner-a", SiteID: "site-a", SiteName: "Original site", AccountID: "account-a", AccountName: "Original account", Action: "one", Outcome: "success", CreatedAt: base},
		{ID: "b-1", OwnerID: "owner-b", Action: "private", Outcome: "failed", CreatedAt: base.Add(4 * time.Hour)},
		{ID: "a-2", OwnerID: "owner-a", Action: "two", Outcome: "success", CreatedAt: base.Add(time.Hour)},
		{ID: "a-3", OwnerID: "owner-a", Action: "three", Outcome: "skipped", CreatedAt: base.Add(24 * time.Hour)},
	}
	for _, record := range records {
		if err := store.Append(record); err != nil {
			t.Fatal(err)
		}
	}

	first, err := store.List("owner-a", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 3 || first.TotalPages != 2 || first.HasPrevious || !first.HasNext {
		t.Fatalf("unexpected first page metadata: %+v", first)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "a-3" || first.Items[1].ID != "a-2" {
		t.Fatalf("unexpected first page items: %+v", first.Items)
	}
	second, err := store.List("owner-a", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "a-1" || !second.HasPrevious || second.HasNext {
		t.Fatalf("unexpected second page: %+v", second)
	}
	if second.Items[0].SiteName != "Original site" || second.Items[0].AccountName != "Original account" {
		t.Fatalf("name snapshots were not retained: %+v", second.Items[0])
	}

	other, err := store.List("owner-b", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if other.Total != 1 || len(other.Items) != 1 || other.Items[0].ID != "b-1" {
		t.Fatalf("owner isolation failed: %+v", other)
	}
	if _, err := os.Stat(filepath.Join(store.Directory(), "audit-2026-07-25.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Directory(), "audit-2026-07-26.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestStoreDeduplicatesMigrationRetriesAndSkipsTornLine(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := Record{ID: "same-id", OwnerID: "owner", Action: "test", Outcome: "success", CreatedAt: time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)}
	if err := store.Append(record); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(record); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory(), "audit-2026-07-26.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{torn"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	page, err := store.List("owner", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("expected one deduplicated record, got %+v", page)
	}
	ids, err := store.IDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected one known ID, got %d", len(ids))
	}
}

func TestStoreRejectsCompletedCorruptRecord(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory(), "audit-2026-07-26.jsonl")
	if err := os.WriteFile(path, []byte("{corrupt}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List("owner", 1, 10); err == nil {
		t.Fatal("expected a completed corrupt line to fail the read")
	}
}

func TestStoreSerializesConcurrentAppends(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const count = 32
	var group sync.WaitGroup
	errors := make(chan error, count)
	for index := 0; index < count; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			errors <- store.Append(Record{
				ID:        fmt.Sprintf("event-%02d", index),
				OwnerID:   "owner",
				Action:    "concurrent",
				Outcome:   "success",
				CreatedAt: time.Date(2026, 7, 26, 12, 0, index, 0, time.UTC),
			})
		}(index)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.List("owner", 1, count)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != count || len(page.Items) != count {
		t.Fatalf("expected %d records, got %+v", count, page)
	}
}

func TestStoreListPagesNewestRecordsWithoutFullScan(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.August, 24, 0, 0, 0, 0, time.UTC)
	const total = 4000
	for index := 0; index < total; index++ {
		if err := store.Append(Record{
			ID:        fmt.Sprintf("event-%04d", index),
			OwnerID:   "owner",
			Action:    "bulk",
			Outcome:   "success",
			CreatedAt: base.Add(time.Duration(index) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.List("owner", 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasNext || len(first.Items) != 5 || first.Items[0].ID != "event-3999" || first.Items[4].ID != "event-3995" {
		t.Fatalf("unexpected newest page: %+v", first.Items)
	}
	second, err := store.List("owner", 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if second.Items[0].ID != "event-3994" || !second.HasPrevious {
		t.Fatalf("unexpected second page: %+v", second.Items)
	}
	last, err := store.List("owner", 800, 5)
	if err != nil {
		t.Fatal(err)
	}
	if last.HasNext || !last.HasPrevious || len(last.Items) != 5 || last.Items[0].ID != "event-0004" || last.Items[4].ID != "event-0000" {
		t.Fatalf("unexpected last page: %+v", last)
	}
}

func TestStorePurgeRemovesOnlyExpiredOwnerFiles(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 24, 15, 0, 0, 0, time.UTC)
	records := []Record{
		{ID: "keep-a", OwnerID: "owner-a", Action: "keep", Outcome: "success", CreatedAt: now.AddDate(0, 0, -2)},
		{ID: "old-a", OwnerID: "owner-a", Action: "old", Outcome: "success", CreatedAt: now.AddDate(0, 0, -10)},
		{ID: "old-b", OwnerID: "owner-b", Action: "old", Outcome: "success", CreatedAt: now.AddDate(0, 0, -10)},
		{ID: "older-a", OwnerID: "owner-a", Action: "older", Outcome: "success", CreatedAt: now.AddDate(0, 0, -20)},
	}
	for _, record := range records {
		if err := store.Append(record); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.Purge("owner-a", 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedRecords != 2 || result.RemovedFiles != 1 || result.RewrittenFiles != 1 {
		t.Fatalf("unexpected purge result: %+v", result)
	}
	keptA, err := store.List("owner-a", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(keptA.Items) != 1 || keptA.Items[0].ID != "keep-a" || keptA.HasNext {
		t.Fatalf("owner-a should only keep recent logs: %+v", keptA)
	}
	keptB, err := store.List("owner-b", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(keptB.Items) != 1 || keptB.Items[0].ID != "old-b" {
		t.Fatalf("owner-b logs must be retained: %+v", keptB)
	}
}
