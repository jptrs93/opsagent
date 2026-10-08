package nodepublisher

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (p *Publisher) SetAcme(state *apigen.AcmeState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acme = state
	if p.closed {
		return
	}
	changed := p.refreshAcmeLocked(nil)
	p.enqueueLocked(outputs{seq: p.appliedSeq, acme: changed})
}

func ingressHostnames(record *apigen.DeploymentRecord, into map[string]struct{}) {
	for _, route := range record.Deployment.Spec.Networking.Ingress {
		if route.Hostname != "" {
			into[route.Hostname] = struct{}{}
		}
	}
}

func (p *Publisher) hostnamesByNodeLocked() map[uint64]map[string]struct{} {
	out := make(map[uint64]map[string]struct{}, len(p.cache.nodes))
	names := func(nodeID uint64) map[string]struct{} {
		set := out[nodeID]
		if set == nil {
			set = make(map[string]struct{})
			out[nodeID] = set
		}
		return set
	}
	for _, inst := range p.cache.instances {
		ingressHostnames(p.cache.versions[versionKey{inst.Deployment.DeploymentID, inst.Deployment.Version}].record, names(inst.NodeID))
	}
	for nodeID, deployments := range p.plan.published {
		for _, deploymentID := range deployments {
			ingressHostnames(p.cache.current[deploymentID], names(nodeID))
		}
	}
	return out
}

func acmeSubset(global *apigen.AcmeState, hostnames map[string]struct{}) apigen.AcmeState {
	subset := apigen.AcmeState{}
	if global == nil {
		return subset
	}
	for _, binding := range global.CertBindings {
		if _, ok := hostnames[binding.Hostname]; ok {
			subset.CertBindings = append(subset.CertBindings, binding)
		}
	}
	for _, challenge := range global.Challenges {
		if _, ok := hostnames[challenge.Hostname]; ok || challenge.Hostname == "" {
			subset.Challenges = append(subset.Challenges, challenge)
		}
	}
	return subset
}

func (p *Publisher) refreshAcmeLocked(nodeIDs []uint64) map[uint64]*apigen.AcmeState {
	if nodeIDs == nil {
		for nodeID := range p.cache.nodes {
			nodeIDs = append(nodeIDs, nodeID)
		}
		for nodeID := range p.acmeByNode {
			if _, ok := p.cache.nodes[nodeID]; !ok {
				delete(p.acmeByNode, nodeID)
			}
		}
	}
	changed := make(map[uint64]*apigen.AcmeState)
	if len(nodeIDs) == 0 {
		return changed
	}
	hostnames := p.hostnamesByNodeLocked()
	for _, nodeID := range nodeIDs {
		subset := acmeSubset(p.acme, hostnames[nodeID])
		sum := sha256.Sum256(subset.Encode())
		hash := hex.EncodeToString(sum[:])
		existing := p.acmeByNode[nodeID]
		if existing != nil && existing.hash == hash {
			continue
		}
		subset.Seq = time.Now().UnixNano()
		if existing != nil && subset.Seq <= existing.state.Seq {
			subset.Seq = existing.state.Seq + 1
		}
		p.acmeByNode[nodeID] = &acmeEntry{state: subset, hash: hash}
		if existing != nil {
			changed[nodeID] = &subset
		}
	}
	return changed
}
