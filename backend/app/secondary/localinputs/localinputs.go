// Package localinputs persists a secondary's runtime input values (secret and
// config plaintexts fetched from the primary) encrypted under this node's own
// machine key.
//
// Why it exists: without it, RuntimeInputs holds those values in process memory
// only, so every secondary restart refetches them over mTLS from the primary. That
// makes a secondary's ability to cold-start its own workloads depend on primary
// availability — which is exactly backwards, since the workloads themselves do
// not.
//
// # Why there is no key hierarchy
//
// The primary needs an SMK, keyslots and a recovery code because losing its
// machine key must not lose the secrets. None of that applies here: the primary
// is authoritative, so a lost or unreadable key just means refetching. That
// reduces the whole design to one machine KEK sealing each row directly, and it
// is why a decrypt failure is not an error worth propagating — the row is
// dropped and refetched.
//
// # What the encryption is and is not for
//
// Against a local attacker who already has the DB file it is weak by
// construction: with the default file provider the machine key sits 0600 beside
// secondary.db, same uid. What it does buy is the offline case — disk images, VM
// snapshots, volume clones, a support bundle, `sqlite3 .dump` pasted into a
// ticket — where ciphertext is a categorically different object to hand around
// than plaintext. It is also what makes the planned TPM-sealed provider a
// one-line swap rather than a migration of every row on every secondary.
package localinputs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
)

// DB is the storage passthrough for the local_runtime_inputs table. It only
// ever sees sealed rows.
type DB interface {
	ListLocalRuntimeInputs() []state.LocalRuntimeInput
	UpsertLocalRuntimeInput(state.LocalRuntimeInput)
	DeleteLocalRuntimeInput(kind, refID, refVersion int64)
}

// Store implements runtimeinputs.Persistence.
type Store struct {
	// ctx is the component root logging context, derived from the caller's
	// context at Open.
	ctx context.Context
	db  DB
	key []byte
}

// Open loads this node's machine key, establishing one on first use, and
// returns a Store bound to it.
//
// Establishing on a missing key is correct here and would not be on the
// primary: a node that has never had a key and one whose key was lost want the
// same thing, because every value the key protects can be refetched. Rows sealed
// under a superseded key simply stop opening and are dropped by Load.
func Open(ctx context.Context, db DB, provider machinekey.Provider) (*Store, error) {
	ctx = logu.AddTag(ctx, "LocalInputs")
	key, err := provider.Load()
	if err != nil || len(key) != machinekey.KeyLen {
		if err == nil {
			err = fmt.Errorf("machine key is %d bytes, want %d", len(key), machinekey.KeyLen)
		}
		slog.InfoContext(ctx, "localinputs: establishing a new machine key", "err", err)
		if key, err = provider.Establish(); err != nil {
			return nil, fmt.Errorf("establishing machine key: %w", err)
		}
	}
	return &Store{ctx: ctx, db: db, key: key}, nil
}

// LoadRuntimeInputs returns every locally stored secret and config value.
//
// A row that will not open is dropped rather than failing the load: it means the
// machine key changed, and the value is refetchable. Failing here instead would
// wedge secondary startup on recoverable local damage.
func (s *Store) LoadRuntimeInputs() (secrets, configs map[apigen.ValueRef]string, err error) {
	secrets = map[apigen.ValueRef]string{}
	configs = map[apigen.ValueRef]string{}
	dropped := 0
	for _, row := range s.db.ListLocalRuntimeInputs() {
		if row.Kind == state.LocalRuntimeInputKindIssuedTLS {
			continue
		}
		plaintext, openErr := machinekey.Open(s.key, row.Ciphertext, row.Nonce, aad(row.Kind, row.RefID, row.RefVersion))
		if openErr != nil {
			s.db.DeleteLocalRuntimeInput(row.Kind, row.RefID, row.RefVersion)
			dropped++
			continue
		}
		ref := rowRef(row)
		switch row.Kind {
		case state.LocalRuntimeInputKindSecret:
			secrets[ref] = string(plaintext)
		case state.LocalRuntimeInputKindConfig:
			configs[ref] = string(plaintext)
		default:
			s.db.DeleteLocalRuntimeInput(row.Kind, row.RefID, row.RefVersion)
			dropped++
		}
	}
	if dropped > 0 {
		slog.WarnContext(s.ctx, fmt.Sprintf("localinputs: dropped %d undecryptable local runtime inputs; they will be refetched from the primary", dropped))
	}
	return secrets, configs, nil
}

func (s *Store) StoreRuntimeInputs(secrets, configs map[apigen.ValueRef]string) error {
	if err := s.storeKind(state.LocalRuntimeInputKindSecret, secrets); err != nil {
		return err
	}
	return s.storeKind(state.LocalRuntimeInputKindConfig, configs)
}

func (s *Store) storeKind(kind int64, values map[apigen.ValueRef]string) error {
	now := time.Now().UnixMilli()
	for ref, value := range values {
		ciphertext, nonce, err := machinekey.Seal(s.key, []byte(value), aad(kind, int64(ref.ID), int64(ref.Version)))
		if err != nil {
			return fmt.Errorf("sealing runtime input kind %d ref %s: %w", kind, ref, err)
		}
		s.db.UpsertLocalRuntimeInput(state.LocalRuntimeInput{
			Kind:       kind,
			RefID:      int64(ref.ID),
			RefVersion: int64(ref.Version),
			Ciphertext: ciphertext,
			Nonce:      nonce,
			FetchedAt:  now,
		})
	}
	return nil
}

// RetainRuntimeInputs deletes every stored value whose ref is absent from the
// given keep sets, and reports how many rows it removed.
func (s *Store) RetainRuntimeInputs(secrets, configs map[apigen.ValueRef]struct{}) (int, error) {
	removed := 0
	for _, row := range s.db.ListLocalRuntimeInputs() {
		keep := false
		switch row.Kind {
		case state.LocalRuntimeInputKindSecret:
			_, keep = secrets[rowRef(row)]
		case state.LocalRuntimeInputKindConfig:
			_, keep = configs[rowRef(row)]
		case state.LocalRuntimeInputKindIssuedTLS:
			continue
		}
		if keep {
			continue
		}
		s.db.DeleteLocalRuntimeInput(row.Kind, row.RefID, row.RefVersion)
		removed++
	}
	return removed, nil
}

func (s *Store) LoadIssuedTLS() (map[int32]*runtimeinputs.IssuedTLSValue, error) {
	out := map[int32]*runtimeinputs.IssuedTLSValue{}
	dropped := 0
	for _, row := range s.db.ListLocalRuntimeInputs() {
		if row.Kind != state.LocalRuntimeInputKindIssuedTLS {
			continue
		}
		plaintext, openErr := machinekey.Open(s.key, row.Ciphertext, row.Nonce, aad(row.Kind, row.RefID, row.RefVersion))
		if openErr != nil {
			s.db.DeleteLocalRuntimeInput(row.Kind, row.RefID, row.RefVersion)
			dropped++
			continue
		}
		var value runtimeinputs.IssuedTLSValue
		if err := json.Unmarshal(plaintext, &value); err != nil {
			s.db.DeleteLocalRuntimeInput(row.Kind, row.RefID, row.RefVersion)
			dropped++
			continue
		}
		out[int32(row.RefID)] = &value
	}
	if dropped > 0 {
		slog.WarnContext(s.ctx, fmt.Sprintf("localinputs: dropped %d unreadable local issued TLS entries; they will be refetched from the primary", dropped))
	}
	return out, nil
}

func (s *Store) StoreIssuedTLS(values map[int32]*runtimeinputs.IssuedTLSValue) error {
	now := time.Now().UnixMilli()
	for id, value := range values {
		plaintext, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encoding issued TLS for deployment %d: %w", id, err)
		}
		ciphertext, nonce, err := machinekey.Seal(s.key, plaintext, aad(state.LocalRuntimeInputKindIssuedTLS, int64(id), 0))
		if err != nil {
			return fmt.Errorf("sealing issued TLS for deployment %d: %w", id, err)
		}
		s.db.UpsertLocalRuntimeInput(state.LocalRuntimeInput{
			Kind:       state.LocalRuntimeInputKindIssuedTLS,
			RefID:      int64(id),
			Ciphertext: ciphertext,
			Nonce:      nonce,
			FetchedAt:  now,
		})
	}
	return nil
}

func (s *Store) RetainIssuedTLS(keep map[int32]struct{}) (int, error) {
	removed := 0
	for _, row := range s.db.ListLocalRuntimeInputs() {
		if row.Kind != state.LocalRuntimeInputKindIssuedTLS {
			continue
		}
		if _, ok := keep[int32(row.RefID)]; ok {
			continue
		}
		s.db.DeleteLocalRuntimeInput(row.Kind, row.RefID, row.RefVersion)
		removed++
	}
	return removed, nil
}

func rowRef(row state.LocalRuntimeInput) apigen.ValueRef {
	return apigen.ValueRef{ID: int32(row.RefID), Version: int32(row.RefVersion)}
}

// aad binds a row's kind, id, and version into its tag, so a ciphertext cannot
// be moved to another value or reinterpreted as another kind.
func aad(kind, refID, refVersion int64) []byte {
	return []byte("opendeploy-local-runtime-input:" + strconv.FormatInt(kind, 10) + ":" + strconv.FormatInt(refID, 10) + ":" + strconv.FormatInt(refVersion, 10))
}
