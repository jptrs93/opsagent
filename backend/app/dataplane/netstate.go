package dataplane

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	odnet "github.com/jptrs93/opsagent/backend/lib/network"
)

type deploymentStore interface {
	FetchDeploymentSnapshot(machine string) []apigen.DeploymentWithStatus
	SubscribeDeploymentUpdates(machine string) (chan apigen.DeploymentWithStatus, func())
}

func NetStatePath(dataDir string) string {
	return filepath.Join(dataDir, "dataplane", "netstate.pb")
}

// RunNetStateWriter writes full protobuf netstate snapshots for the local
// dataplane process. It is intentionally full-state and idempotent.
func RunNetStateWriter(ctx context.Context, store deploymentStore, machine, path string) {
	seq := int64(0)
	write := func() {
		seq++
		if err := WriteNetState(path, RenderNetState(seq, machine, store.FetchDeploymentSnapshot(machine))); err != nil {
			slog.Warn("writing dataplane netstate failed", "path", path, "err", err)
		}
	}
	write()
	updates, unsub := store.SubscribeDeploymentUpdates(machine)
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-updates:
			if !ok {
				return
			}
			write()
		}
	}
}

func RenderNetState(seq int64, machine string, items []apigen.DeploymentWithStatus) *apigen.NetState {
	prefix, _ := odnet.Default.PrefixValue()
	services := make([]*apigen.DnsService, 0, len(items))
	for _, item := range items {
		if item.Config.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			continue
		}
		ready := readyEndpoints(item.Status.Runner.Endpoints)
		if len(ready) == 0 {
			continue
		}
		services = append(services, &apigen.DnsService{
			Name:        dnsLabel(item.Config.ConfigID.Name),
			Environment: spaceDNSName(item.Config.ConfigID.SpaceID),
			Endpoints:   ready,
		})
	}
	return &apigen.NetState{
		Seq:               seq,
		UlaPrefix:         prefix.Bytes(),
		Machine:           machine,
		DnsServices:       services,
		UpstreamResolvers: hostResolvers(),
	}
}

func WriteNetState(path string, state *apigen.NetState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, state.Encode(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readyEndpoints(in []*apigen.Endpoint) []*apigen.Endpoint {
	out := make([]*apigen.Endpoint, 0, len(in))
	for _, ep := range in {
		if ep != nil && ep.State == apigen.EndpointState_ENDPOINT_READY {
			copy := *ep
			out = append(out, &copy)
		}
	}
	return out
}

func dnsLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if r == '-' && !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "deployment"
	}
	return out
}

func spaceDNSName(id int32) string {
	switch id {
	case 0:
		return "opendeploy"
	case 1:
		return "default"
	default:
		return "space-" + strconv.Itoa(int(id))
	}
}

func hostResolvers() []string {
	b, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "nameserver" {
			out = append(out, fields[1])
		}
	}
	return out
}
