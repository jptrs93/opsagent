package apigen

import "testing"

func TestLegacyDeploymentPayloadsFailV2ClusterDecode(t *testing.T) {
	legacy := (&DeploymentConfig{ID: 42, NodeID: 7, Identity: DeploymentIdentity{SpaceID: 1, Name: "app"}}).Encode()

	update := AppendTag(nil, 2, BytesType)
	update = AppendBytes(update, legacy)
	if _, err := DecodeMsgToWorker(update); err == nil {
		t.Fatal("V2 decoder accepted a V1 deployment update")
	}

	item := AppendTag(nil, 1, BytesType)
	item = AppendBytes(item, legacy)
	snapshot := AppendTag(nil, 1, BytesType)
	snapshot = AppendBytes(snapshot, item)
	message := AppendTag(nil, 1, BytesType)
	message = AppendBytes(message, snapshot)
	if _, err := DecodeMsgToWorker(message); err == nil {
		t.Fatal("V2 decoder accepted a V1 deployment snapshot")
	}
}

func TestV2DeploymentPayloadFailsLegacyDecode(t *testing.T) {
	v2 := (&DeploymentConfig2{ID: 42, NodeID: 7, Identity: DeploymentIdentity{SpaceID: 1, Name: "app"}}).Encode()
	if _, err := DecodeDeploymentConfig(v2); err == nil {
		t.Fatal("V1 decoder accepted a V2 deployment config")
	}
}
