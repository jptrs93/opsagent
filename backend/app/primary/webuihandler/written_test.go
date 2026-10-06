package webuihandler

import (
	"context"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestWriteEndpointsReturnTheWrittenEntityUnderItsSeq(t *testing.T) {
	h, user := newAuthTestHandler(t)
	ctx := apigen.Context{Ctx: context.Background(), User: user}
	created, err := h.PostV1ConfigsCreate(ctx, &apigen.ConfigCreateRequest{Key: "app.conf", Value: "one"})
	if err != nil {
		t.Fatal(err)
	}
	seq, err := h.Store.Queries().GetGlobalSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Mutations) != 1 || created.Mutations[0].Value.Create == nil || created.Seq != seq || int64(created.Actor) != int64(user.ID) {
		t.Fatalf("create response = %+v, want one create under seq %d by user %d", created, seq, user.ID)
	}
	id, cfg, meta := created.Mutations[0].EntityID(), created.Mutations[0].Entity().Value.Config, created.Mutations[0].Meta()
	if cfg == nil || cfg.Value != "one" || meta == nil || meta.ValueVersion != 1 || meta.CreatedTime != created.Time || meta.UpdatedSeq != created.Seq || int64(meta.UpdatedActor) != int64(user.ID) {
		t.Fatalf("created config = %+v with meta %+v", cfg, meta)
	}
	same, err := h.PostV1ConfigsSet(ctx, &apigen.ConfigSetRequest{ConfigID: id, Value: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if same.Seq != created.Seq || len(same.Mutations) != 1 || same.Mutations[0].Value.Update == nil || same.Mutations[0].Meta() == nil || same.Mutations[0].Meta().ValueVersion != 1 || same.Mutations[0].Meta().UpdatedSeq != created.Seq {
		t.Fatalf("no-op set response = %+v, want the current row under seq %d", same, created.Seq)
	}
	if after, _ := h.Store.Queries().GetGlobalSeq(ctx); after != seq {
		t.Fatalf("no-op set consumed a sequence: %d -> %d", seq, after)
	}
	secret, err := h.PostV1SecretsCreate(ctx, &apigen.SecretCreateRequest{Key: "db", Value: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	s := secret.Mutations[0].Entity().Value.Secret
	if s == nil || s.Sealed.Present || secret.Mutations[0].Meta().ValueVersion != 1 {
		t.Fatalf("secret create response carries sealed bytes: %+v", s)
	}
}
