package pq

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestUpdateSecretSealRewritesLoggedPayloads(t *testing.T) {
	q := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer q.Close()
	ctx := context.Background()
	created, err := q.InsertSecretEvent(ctx, SecretEvent{GlobalSeq: 1, EventTime: 1, CreatedTime: 1, SecretID: 1, Version: 1, ValueVersion: 1, ValueChanged: 1,
		Name: "token", SpaceID: 1, SmkVersion: 1, Ciphertext: []byte{1}, Nonce: []byte{2}, EventType: EventCreate})
	if err != nil {
		t.Fatal(err)
	}
	old := SealedValue{SmkVersion: 1, Ciphertext: []byte{1}, Nonce: []byte{2}}
	if err := q.InsertWriteEvent(ctx, Events([]Mutation{SecretMutation(created, old)})[0]); err != nil {
		t.Fatal(err)
	}
	renamed, carried, err := q.InsertSecretCarryEvent(ctx, SecretEvent{GlobalSeq: 2, EventTime: 2, CreatedTime: 1, SecretID: 1, Version: 2, ValueVersion: 1, Name: "renamed", SpaceID: 1, EventType: EventUpdate})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.InsertWriteEvent(ctx, Events([]Mutation{SecretMutation(renamed, carried)})[0]); err != nil {
		t.Fatal(err)
	}
	second, err := q.InsertSecretEvent(ctx, SecretEvent{GlobalSeq: 3, EventTime: 3, CreatedTime: 1, SecretID: 1, Version: 3, ValueVersion: 2, ValueChanged: 1,
		Name: "renamed", SpaceID: 1, SmkVersion: 1, Ciphertext: []byte{3}, Nonce: []byte{4}, EventType: EventUpdate})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.InsertWriteEvent(ctx, Events([]Mutation{SecretMutation(second, SealedValue{SmkVersion: 1, Ciphertext: []byte{3}, Nonce: []byte{4}})})[0]); err != nil {
		t.Fatal(err)
	}

	seals, err := q.ListSecretSealRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateSecretSeal(ctx, seals[0], []byte{9}, []byte{8}); err != nil {
		t.Fatal(err)
	}

	events, err := q.WriteEventsInRange(ctx, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("log holds %d events, want 3", len(events))
	}
	for i, want := range [][]byte{{9}, {9}, {3}} {
		got := events[i].Mutations[0].Entity().Secret
		if !bytes.Equal(got.Ciphertext, want) {
			t.Fatalf("seq %d ciphertext = %v, want %v", events[i].Seq, got.Ciphertext, want)
		}
		if bytes.Equal(want, []byte{9}) && !bytes.Equal(got.Nonce, []byte{8}) {
			t.Fatalf("seq %d nonce = %v, want the new nonce", events[i].Seq, got.Nonce)
		}
	}
	rows, err := q.ListSecretSealRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rows[0].Ciphertext, []byte{9}) || !bytes.Equal(rows[1].Ciphertext, []byte{1}) || !bytes.Equal(rows[2].Ciphertext, []byte{3}) {
		t.Fatalf("table rows after the re-seal = %+v", rows)
	}
}
