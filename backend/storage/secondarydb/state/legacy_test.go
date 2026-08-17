package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/sq"
)

// encodeLegacyScheduledInstanceState builds a ScheduledInstanceState blob the
// way pre-v0.0.444 binaries wrote it: the config's space/name inside the
// since-removed DeploymentConfig.identity sub-message (field 3, with space_id
// = 1 and name = 3), and no flat space_id/name fields.
func encodeLegacyScheduledInstanceState(instanceID, deploymentID, nodeID, spaceID int32, name string) []byte {
	var identity []byte
	identity = apigen.AppendTag(identity, 1, apigen.VarintType)
	identity = apigen.AppendVarint(identity, uint64(spaceID))
	identity = apigen.AppendTag(identity, 3, apigen.BytesType)
	identity = apigen.AppendBytes(identity, []byte(name))

	var cfg []byte
	cfg = apigen.AppendTag(cfg, 1, apigen.VarintType)
	cfg = apigen.AppendVarint(cfg, uint64(deploymentID))
	cfg = apigen.AppendTag(cfg, 2, apigen.VarintType)
	cfg = apigen.AppendVarint(cfg, uint64(nodeID))
	cfg = apigen.AppendTag(cfg, 3, apigen.BytesType)
	cfg = apigen.AppendBytes(cfg, identity)
	cfg = apigen.AppendTag(cfg, 7, apigen.VarintType)
	cfg = apigen.AppendVarint(cfg, 4)

	var inst []byte
	inst = apigen.AppendTag(inst, 1, apigen.VarintType)
	inst = apigen.AppendVarint(inst, uint64(instanceID))
	inst = apigen.AppendTag(inst, 3, apigen.VarintType)
	inst = apigen.AppendVarint(inst, uint64(deploymentID))
	inst = apigen.AppendTag(inst, 7, apigen.VarintType)
	inst = apigen.AppendVarint(inst, uint64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING))

	var state []byte
	state = apigen.AppendTag(state, 1, apigen.BytesType)
	state = apigen.AppendBytes(state, inst)
	state = apigen.AppendTag(state, 2, apigen.BytesType)
	state = apigen.AppendBytes(state, cfg)
	return state
}

// encodeIdentitylessScheduledInstanceState builds the blob shape a pre-v0.0.444
// worker persisted while connected to a newer primary: it decoded the flat
// space/name fields as unknown and re-encoded the config with neither the
// legacy identity sub-message nor the flat fields — only id, node, version,
// and spec survive.
func encodeIdentitylessScheduledInstanceState(instanceID, deploymentID, nodeID int32, spec *apigen.DeploymentSpec) []byte {
	var cfg []byte
	cfg = apigen.AppendTag(cfg, 1, apigen.VarintType)
	cfg = apigen.AppendVarint(cfg, uint64(deploymentID))
	cfg = apigen.AppendTag(cfg, 2, apigen.VarintType)
	cfg = apigen.AppendVarint(cfg, uint64(nodeID))
	cfg = apigen.AppendTag(cfg, 7, apigen.VarintType)
	cfg = apigen.AppendVarint(cfg, 4)
	cfg = apigen.AppendTag(cfg, 8, apigen.BytesType)
	cfg = apigen.AppendBytes(cfg, spec.Encode())

	var inst []byte
	inst = apigen.AppendTag(inst, 1, apigen.VarintType)
	inst = apigen.AppendVarint(inst, uint64(instanceID))
	inst = apigen.AppendTag(inst, 3, apigen.VarintType)
	inst = apigen.AppendVarint(inst, uint64(deploymentID))
	inst = apigen.AppendTag(inst, 7, apigen.VarintType)
	inst = apigen.AppendVarint(inst, uint64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING))

	var state []byte
	state = apigen.AppendTag(state, 1, apigen.BytesType)
	state = apigen.AppendBytes(state, inst)
	state = apigen.AppendTag(state, 2, apigen.BytesType)
	state = apigen.AppendBytes(state, cfg)
	return state
}

func TestIdentitylessCachedBlobsInferInternalIdentity(t *testing.T) {
	netproxySpec := internaldeploy.NetproxySpec()
	if err := netproxySpec.SetWorkloadState("v0.0.443", true); err != nil {
		t.Fatal(err)
	}
	selfSpec := internaldeploy.SelfSpec()
	if err := selfSpec.SetWorkloadState("v0.0.443", true); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	q := sq.Open(dbPath)
	for id, blob := range map[int64][]byte{
		41: encodeIdentitylessScheduledInstanceState(41, 12, 2, netproxySpec),
		42: encodeIdentitylessScheduledInstanceState(42, 11, 2, selfSpec),
	} {
		if err := q.UpsertLocalScheduledInstanceCache(context.Background(), sq.UpsertLocalScheduledInstanceCacheParams{
			InstanceID: id,
			Blob:       blob,
		}); err != nil {
			t.Fatalf("seed cache row %d: %v", id, err)
		}
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	store := Open(dbPath)
	defer store.Close()
	var netproxyID, selfID int32
	for _, item := range store.FetchScheduledSnapshot(nil) {
		if internaldeploy.IsNetproxyConfig(&item.Config) {
			netproxyID = item.Config.ID
		}
		if internaldeploy.IsSelfConfig(&item.Config) {
			selfID = item.Config.ID
		}
	}
	if netproxyID != 12 {
		t.Fatalf("netproxy inferred from identityless blob = deployment %d, want 12", netproxyID)
	}
	if selfID != 11 {
		t.Fatalf("self deployment inferred from identityless blob = deployment %d, want 11", selfID)
	}
}

func TestLegacyAssignmentBlobsKeepTheirIdentity(t *testing.T) {
	blob := encodeLegacyScheduledInstanceState(31, 12, 2, internaldeploy.SpaceID, internaldeploy.NetproxyName)

	decoded, err := apigen.DecodeScheduledInstanceState(blob)
	if err != nil {
		t.Fatalf("decode legacy blob: %v", err)
	}
	if decoded.Config.Name != "" {
		t.Fatalf("modern decoder read legacy identity name %q; fallback is dead code", decoded.Config.Name)
	}

	spaceID, name, ok := legacyDeploymentIdentity(blob)
	if !ok || spaceID != internaldeploy.SpaceID || name != internaldeploy.NetproxyName {
		t.Fatalf("legacyDeploymentIdentity = (%d, %q, %v), want (%d, %q, true)",
			spaceID, name, ok, internaldeploy.SpaceID, internaldeploy.NetproxyName)
	}

	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	q := sq.Open(dbPath)
	if err := q.UpsertLocalScheduledInstanceCache(context.Background(), sq.UpsertLocalScheduledInstanceCacheParams{
		InstanceID: 31,
		Blob:       blob,
	}); err != nil {
		t.Fatalf("seed legacy cache row: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	store := Open(dbPath)
	defer store.Close()
	var netproxy *apigen.ScheduledInstanceState
	for _, item := range store.FetchScheduledSnapshot(nil) {
		if internaldeploy.IsNetproxyConfig(&item.Config) {
			cp := item
			netproxy = &cp
		}
	}
	if netproxy == nil {
		t.Fatal("cached legacy netproxy assignment was not identified after reopen")
	}
	if netproxy.Config.ID != 12 || netproxy.Config.NodeID != 2 {
		t.Fatalf("netproxy config = id %d node %d, want id 12 node 2", netproxy.Config.ID, netproxy.Config.NodeID)
	}
}
