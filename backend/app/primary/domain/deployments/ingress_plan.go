package deployments

import (
	"context"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/ingressplan"
)

func ReservationsFromSettings(primaryNodeID int32, settings *apigen.ClusterSettings, str func(apigen.StringSetting) string, boolean func(apigen.BoolSetting) bool) []ingressplan.Reservation {
	return ingressplan.WebUIReservations(primaryNodeID,
		boolean(settings.HttpsWeb.Enabled), str(settings.HttpsWeb.Listen),
		boolean(settings.HttpWeb.Enabled), str(settings.HttpWeb.Listen))
}

// ingressPlanInputs builds the evaluator's inputs from live state, replacing
// (or adding) the candidate deployment's spec.
func ingressPlanInputs(live nodes.LiveState, reservations []ingressplan.Reservation, nodeID, deploymentID int32, candidate *apigen.DeploymentSpec) ingressplan.Inputs {
	in := ingressplan.Inputs{Reservations: reservations, Candidate: deploymentID}
	for _, node := range live.Nodes {
		in.Nodes = append(in.Nodes, ingressplan.Node{ID: node.ID, HostAddresses: ingressplan.ParseHostAddresses(node.HostAddresses)})
	}
	for _, cfg := range live.Deployments {
		// The opendeploy system deployments carry no routes and would name
		// themselves as host-mode neighbours on every node.
		if cfg.DeploymentID == deploymentID || internaldeploy.IsInternalConfig(cfg) {
			continue
		}
		in.Deployments = append(in.Deployments, ingressplan.DeploymentFromSpec(cfg.DeploymentID, cfg.Value.NodeID, cfg.Value.Name, &cfg.Value.Spec))
	}
	if candidate != nil {
		name := ""
		if existing := live.Deployments[deploymentID]; existing != nil {
			name = existing.Value.Name
		}
		in.Deployments = append(in.Deployments, ingressplan.DeploymentFromSpec(deploymentID, nodeID, name, candidate))
	}
	return in
}

// ValidateNodeNetworkingClaims evaluates the candidate against every other
// deployment and the Web UI reservations and rejects the save on the first
// error. A node selector must name a registered node, and until netproxy
// can dial backends on other machines it must be the deployment's own node:
// a route named for another node would validate and then publish nothing.
func ValidateNodeNetworkingClaims(live nodes.LiveState, reservations []ingressplan.Reservation, nodeID, deploymentID int32, candidate *apigen.DeploymentSpec) error {
	if candidate == nil || candidate.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
		return nil
	}
	for _, route := range candidate.Networking.Ingress {
		if route == nil {
			continue
		}
		for _, entry := range route.Listen {
			if entry == nil || entry.Node == nil || entry.Node.NodeID == 0 {
				continue
			}
			node := live.Nodes[entry.Node.NodeID]
			if node == nil {
				return InvalidConfigErrf("networking.ingress.listen.node: unknown node id %d", entry.Node.NodeID)
			}
			if entry.Node.NodeID != nodeID {
				return InvalidConfigErrf("networking.ingress.listen.node: ingress is served by the deployment's own node only; node %q cannot publish a route for a deployment on another node", node.Name)
			}
		}
	}
	// A deployment being created has no id yet; evaluate it under a sentinel
	// above every stored id so ordering rules still treat it as the newcomer.
	evalID := deploymentID
	if evalID == 0 {
		for id := range live.Deployments {
			if id >= evalID {
				evalID = id + 1
			}
		}
		if evalID == 0 {
			evalID = 1
		}
	}
	result := ingressplan.Evaluate(ingressPlanInputs(live, reservations, nodeID, evalID, candidate))
	for _, diag := range result.Errors {
		if diag.DeploymentID == evalID {
			return InvalidConfigErrf("%s", diag.Message)
		}
	}
	return nil
}

// ValidateIngressAgainstSettings rejects a settings change whose Web UI
// listeners would turn an existing literal ingress claim into an error. The
// caller holds the global store lock (SystemConfig.LockForUpdate).
func ValidateIngressAgainstSettings(ctx context.Context, q *pq.Queries, primaryNodeID int32, resolved *apigen.ClusterSettings) error {
	reservations := ReservationsFromSettings(primaryNodeID, resolved,
		func(v apigen.StringSetting) string { return v.Value },
		func(v apigen.BoolSetting) bool { return v.Value })
	live, err := nodes.ReadLiveState(ctx, q)
	if err != nil {
		return err
	}
	result := ingressplan.Evaluate(ingressPlanInputs(live, reservations, 0, 0, nil))
	for _, diag := range result.Errors {
		name := fmt.Sprintf("deployment %d", diag.DeploymentID)
		if cfg := live.Deployments[diag.DeploymentID]; cfg != nil {
			name = fmt.Sprintf("deployment %q", cfg.Value.Name)
		}
		return fmt.Errorf("%s: %s", name, diag.Message)
	}
	return nil
}
