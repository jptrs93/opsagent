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
