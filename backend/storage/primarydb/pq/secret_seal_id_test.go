package pq

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSecretCarryEventCopiesSealID(t *testing.T) {
	q := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer q.Close()
	ctx := context.Background()
	created, err := q.InsertSecretEvent(ctx, SecretEvent{GlobalSeq: 1, EventTime: 1, CreatedTime: 1, SecretID: 1, Version: 1, ValueVersion: 1, ValueChanged: 1,
		Name: "token", SpaceID: 1, SmkVersion: 1, Ciphertext: []byte{1}, Nonce: []byte{2}, SealID: "kabc", EventType: EventCreate})
	if err != nil {
		t.Fatal(err)
	}
	if created.Value.SealID != "kabc" {
		t.Fatalf("created seal id = %q, want kabc", created.Value.SealID)
	}
	carried, err := q.InsertSecretCarryEvent(ctx, SecretEvent{GlobalSeq: 2, EventTime: 2, CreatedTime: 1, SecretID: 1, Version: 2, ValueVersion: 1,
		Name: "renamed", SpaceID: 1, EventType: EventUpdate})
	if err != nil {
		t.Fatal(err)
	}
	if carried.Value.SealID != "kabc" || carried.Value.Fs.Name != "renamed" {
		t.Fatalf("carried event = %+v, want seal id kabc and name renamed", carried)
	}
	records, err := q.ListSecretVersionRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].SealID != "kabc" || records[0].Name != "renamed" {
		t.Fatalf("version records = %+v", records)
	}
}

func TestSealIDBackfillForLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	q := Open(path)
	ctx := context.Background()
	if _, err := q.db.ExecContext(ctx, `INSERT INTO secret_event_log (
		global_seq, event_time, created_time, author, secret_id, version, value_version, value_changed,
		name, value_directory_id, space_id, smk_version, ciphertext, nonce, event_type
	) VALUES (1, 1, 1, 0, 7, 1, 1, 1, 'legacy', 0, 1, 1, x'01', x'02', 1),
	         (2, 2, 1, 0, 7, 2, 2, 1, 'legacy', 0, 1, 1, x'03', x'04', 2)`); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	q = Open(path)
	defer q.Close()
	records, err := q.ListSecretVersionRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].SealID != "v1" || records[1].SealID != "v2" {
		t.Fatalf("backfilled seal ids = %+v, want v1 and v2", records)
	}
	latest, err := q.GetLatestSecretEvent(ctx, 7)
	if err != nil || latest.Value.SealID != "v2" {
		t.Fatalf("latest event seal id = %q, %v; want v2", latest.Value.SealID, err)
	}
}
