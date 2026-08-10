// Package localinputs persists a secondary's runtime input values (secret and
// config plaintexts fetched from the primary) encrypted under this node's own
// machine key.
//
// Why it exists: without it, RuntimeInputs holds those values in process memory
// only, so every worker restart refetches them over mTLS from the primary. That
// makes a worker's ability to cold-start its own workloads depend on primary
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
// one-line swap rather than a migration of every row on every worker.
package localinputs

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb"
)

// DB is the storage passthrough for the local_runtime_inputs table. It only
// ever sees sealed rows.
type DB interface {
	ListLocalRuntimeInputs() []secondarydb.LocalRuntimeInput
	UpsertLocalRuntimeInput(secondarydb.LocalRuntimeInput)
	DeleteLocalRuntimeInput(kind, refID int64)
}

// Store implements runtimeinputs.Persistence.
type Store struct {
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
func Open(db DB, provider machinekey.Provider) (*Store, error) {
	key, err := provider.Load()
	if err != nil || len(key) != machinekey.KeyLen {
		if err == nil {
			err = fmt.Errorf("machine key is %d bytes, want %d", len(key), machinekey.KeyLen)
		}
		slog.Info("localinputs: establishing a new machine key", "reason", err)
		if key, err = provider.Establish(); err != nil {
			return nil, fmt.Errorf("establishing machine key: %w", err)
		}
	}
	return &Store{db: db, key: key}, nil
}

// LoadRuntimeInputs returns every locally stored secret and config value.
//
// A row that will not open is dropped rather than failing the load: it means the
// machine key changed, and the value is refetchable. Failing here instead would
// wedge worker startup on recoverable local damage.
func (s *Store) LoadRuntimeInputs() (secrets, configs map[int32]string, err error) {
	secrets = map[int32]string{}
	configs = map[int32]string{}
	dropped := 0
	for _, row := range s.db.ListLocalRuntimeInputs() {
		plaintext, openErr := machinekey.Open(s.key, row.Ciphertext, row.Nonce, aad(row.Kind, row.RefID))
		if openErr != nil {
			s.db.DeleteLocalRuntimeInput(row.Kind, row.RefID)
			dropped++
			continue
		}
		switch row.Kind {
		case secondarydb.LocalRuntimeInputKindSecret:
			secrets[int32(row.RefID)] = string(plaintext)
		case secondarydb.LocalRuntimeInputKindConfig:
			configs[int32(row.RefID)] = string(plaintext)
		default:
			s.db.DeleteLocalRuntimeInput(row.Kind, row.RefID)
			dropped++
		}
	}
	if dropped > 0 {
		slog.Warn("localinputs: dropped undecryptable local runtime inputs; they will be refetched from the primary", "count", dropped)
	}
	return secrets, configs, nil
}

// StoreRuntimeInputs seals and persists the given values.
func (s *Store) StoreRuntimeInputs(secrets, configs map[int32]string) error {
	if err := s.storeKind(secondarydb.LocalRuntimeInputKindSecret, secrets); err != nil {
		return err
	}
	return s.storeKind(secondarydb.LocalRuntimeInputKindConfig, configs)
}

func (s *Store) storeKind(kind int64, values map[int32]string) error {
	now := time.Now().UnixMilli()
	for id, value := range values {
		ciphertext, nonce, err := machinekey.Seal(s.key, []byte(value), aad(kind, int64(id)))
		if err != nil {
			return fmt.Errorf("sealing runtime input kind %d id %d: %w", kind, id, err)
		}
		s.db.UpsertLocalRuntimeInput(secondarydb.LocalRuntimeInput{
			Kind:       kind,
			RefID:      int64(id),
			Ciphertext: ciphertext,
			Nonce:      nonce,
			FetchedAt:  now,
		})
	}
	return nil
}

// RetainRuntimeInputs deletes every stored value whose id is absent from the
// given keep sets, and reports how many rows it removed.
func (s *Store) RetainRuntimeInputs(secrets, configs map[int32]struct{}) (int, error) {
	removed := 0
	for _, row := range s.db.ListLocalRuntimeInputs() {
		keep := false
		switch row.Kind {
		case secondarydb.LocalRuntimeInputKindSecret:
			_, keep = secrets[int32(row.RefID)]
		case secondarydb.LocalRuntimeInputKindConfig:
			_, keep = configs[int32(row.RefID)]
		}
		if keep {
			continue
		}
		s.db.DeleteLocalRuntimeInput(row.Kind, row.RefID)
		removed++
	}
	return removed, nil
}

// aad binds a row's kind and id into its tag, so a ciphertext cannot be moved to
// another id or reinterpreted as the other kind.
func aad(kind, refID int64) []byte {
	return []byte("opendeploy-local-runtime-input:" + strconv.FormatInt(kind, 10) + ":" + strconv.FormatInt(refID, 10))
}
