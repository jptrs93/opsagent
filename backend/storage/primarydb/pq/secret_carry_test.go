package pq

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestSecretCarryEventCopiesSealedPayload(t *testing.T) {
	q := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer q.Close()
	ctx := context.Background()
	if _, err := q.InsertSecretEvent(ctx, SecretEvent{GlobalSeq: 1, EventTime: 1, CreatedTime: 1, SecretID: 1, Version: 1, ValueVersion: 1, ValueChanged: 1,
		Name: "token", SpaceID: 1, SmkVersion: 1, Ciphertext: []byte{1}, Nonce: []byte{2}, EventType: EventCreate}); err != nil {
		t.Fatal(err)
	}
	carried, sealed, err := q.InsertSecretCarryEvent(ctx, SecretEvent{GlobalSeq: 2, EventTime: 2, CreatedTime: 1, SecretID: 1, Version: 2, ValueVersion: 1,
		Name: "renamed", SpaceID: 1, EventType: EventUpdate})
	if err != nil {
		t.Fatal(err)
	}
	if carried.Value.Fs.Name != "renamed" || carried.ValueVersion != 1 || carried.Value.ValueVersion != 1 || carried.Value.CreatedTime != 1 {
		t.Fatalf("carried event = %+v", carried)
	}
	if sealed.SmkVersion != 1 || !bytes.Equal(sealed.Ciphertext, []byte{1}) || !bytes.Equal(sealed.Nonce, []byte{2}) {
		t.Fatalf("carried seal = %+v", sealed)
	}
	if got, want := SecretMutation(carried, sealed).Entity.Secret, (apigen.Secret{Fs: &apigen.SecretFs{Name: "renamed"}, SpaceID: 1, ValueVersion: 1, CreatedTime: 1, SmkVersion: 1, Ciphertext: []byte{1}, Nonce: []byte{2}}); !bytes.Equal(got.Encode(), want.Encode()) {
		t.Fatalf("carried mutation payload = %+v", got)
	}
	var smkVersion int64
	var ciphertext, nonce []byte
	if err := q.db.QueryRowContext(ctx, `SELECT smk_version, ciphertext, nonce FROM secret_event_log WHERE secret_id = 1 AND version = 2`).Scan(&smkVersion, &ciphertext, &nonce); err != nil {
		t.Fatal(err)
	}
	if smkVersion != 1 || !bytes.Equal(ciphertext, []byte{1}) || !bytes.Equal(nonce, []byte{2}) {
		t.Fatalf("carried row seal = %d %v %v", smkVersion, ciphertext, nonce)
	}
	records, err := q.ListSecretVersionRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Name != "renamed" {
		t.Fatalf("version records = %+v", records)
	}
}
