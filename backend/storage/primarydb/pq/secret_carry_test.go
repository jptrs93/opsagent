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
	seals, err := q.ListSecretSealRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(seals) != 2 || !bytes.Equal(seals[1].Ciphertext, []byte{1}) || !bytes.Equal(seals[1].Nonce, []byte{2}) {
		t.Fatalf("seal rows = %+v", seals)
	}
	records, err := q.ListSecretVersionRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Name != "renamed" {
		t.Fatalf("version records = %+v", records)
	}
}
