//go:build linux

package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/google/nftables"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const cgroupMount = "/sys/fs/cgroup"

// stopContainers kills and deletes every container in the opendeploy containerd
// namespace through the containerd API, so containerd itself unmounts the
// rootfs snapshots and removes the task cgroups. It needs containerd up; the
// caller falls back to the cgroup and process sweeps when it is not.
func stopContainers() error {
	if !pathExists(containerdSocket) {
		info("containerd socket %s is absent; nothing to stop through the API", containerdSocket)
		return nil
	}
	client, err := containerd.New(containerdSocket, containerd.WithTimeout(5*time.Second))
	if err != nil {
		return fmt.Errorf("connecting to containerd: %w", err)
	}
	defer client.Close()
	ctx := namespaces.WithNamespace(context.Background(), containerdNamespace)

	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	containers, err := client.Containers(listCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}
	if len(containers) == 0 {
		info("no containers in namespace %s", containerdNamespace)
		return nil
	}
	for _, container := range containers {
		id := container.ID()
		if dryRun {
			planned("kill and delete container %s", id)
			continue
		}
		if err := removeContainer(ctx, container); err != nil {
			return fmt.Errorf("removing container %s: %w", id, err)
		}
		info("removed container %s", id)
	}
	return nil
}

func removeContainer(ctx context.Context, container containerd.Container) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if task, err := container.Task(ctx, nil); err == nil {
		_ = task.Kill(ctx, syscall.SIGKILL, containerd.WithKillAll)
		if exited, err := task.Wait(ctx); err == nil {
			select {
			case <-exited:
			case <-time.After(15 * time.Second):
			}
		}
		if _, err := task.Delete(ctx, containerd.WithProcessKill); err != nil {
			return fmt.Errorf("deleting task: %w", err)
		}
	}
	return container.Delete(ctx, containerd.WithSnapshotCleanup)
}

// stopUnitProcesses kills whatever a stopped unit left behind. Both units use
// KillMode=process, so `systemctl stop` only signals the main process:
// containerd leaves its shims (and with them the containers) running, and
// opendeploy leaves nix builds, the backup child, and log consumers.
func stopUnitProcesses(unit string) error {
	for _, dir := range cgroupDirs(unitControlGroup(unit)) {
		// systemd owns the unit's cgroup directory and removes it once empty.
		n, err := killCgroupTree(dir, false)
		if err != nil {
			return fmt.Errorf("stopping leftover processes of %s: %w", unit, err)
		}
		if n > 0 {
			info("killed %d leftover process(es) of %s", n, unit)
		}
	}
	return nil
}

// killContainerProcesses kills every process still inside a container cgroup
// (containerd's /<namespace>/<id> subtree) and removes the cgroups. This is the
// fallback for containers the API teardown could not reach.
func killContainerProcesses() error {
	for _, dir := range cgroupDirs(containerCgroup) {
		n, err := killCgroupTree(dir, true)
		if err != nil {
			return fmt.Errorf("stopping container processes: %w", err)
		}
		if n > 0 {
			info("killed %d container process(es) under cgroup %s", n, dir)
		}
	}
	return nil
}

func unitControlGroup(unit string) string {
	out, err := exec.Command("systemctl", "show", "-p", "ControlGroup", "--value", unit).Output()
	if cg := strings.TrimSpace(string(out)); err == nil && cg != "" {
		return cg
	}
	return "/system.slice/" + unit
}

// cgroupDirs maps a hierarchy-relative cgroup path to the directories that
// hold its processes: the unified v2 tree, or the pids controller on a v1
// host (enough to find and kill them; other v1 controllers keep empty dirs).
func cgroupDirs(rel string) []string {
	if pathExists(filepath.Join(cgroupMount, "cgroup.controllers")) {
		return []string{filepath.Join(cgroupMount, rel)}
	}
	return []string{filepath.Join(cgroupMount, "pids", rel)}
}

// killCgroupTree SIGKILLs every process in the cgroup and its descendants,
// waits for them to exit, and (when removeDirs is set) removes the then-empty
// directories leaf first. Returns how many processes it found.
func killCgroupTree(dir string, removeDirs bool) (int, error) {
	if !pathExists(dir) {
		return 0, nil
	}
	pids, err := cgroupPIDs(dir)
	if err != nil {
		return 0, err
	}
	if dryRun {
		if len(pids) > 0 {
			planned("kill %d process(es) in cgroup %s", len(pids), dir)
		}
		if removeDirs {
			planned("remove cgroup %s", dir)
		}
		return 0, nil
	}
	if len(pids) > 0 {
		// cgroup.kill (Linux 5.14+) kills the whole subtree atomically.
		if err := os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0); err != nil {
			for _, pid := range pids {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			left, _ := cgroupPIDs(dir)
			if len(left) == 0 {
				break
			}
			if time.Now().After(deadline) {
				return len(pids), fmt.Errorf("%d process(es) in %s did not exit after SIGKILL", len(left), dir)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if removeDirs {
		removeCgroupDirs(dir)
	}
	return len(pids), nil
}

func cgroupPIDs(dir string) ([]int, error) {
	seen := map[int]bool{}
	var pids []int
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(filepath.Join(path, "cgroup.procs"))
		if err != nil {
			return nil
		}
		for _, field := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(field); err == nil && !seen[pid] {
				seen[pid] = true
				pids = append(pids, pid)
			}
		}
		return nil
	})
	return pids, err
}

func removeCgroupDirs(dir string) {
	var dirs []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = os.Remove(d)
	}
}

// killStrayProcesses SIGKILLs any remaining process whose executable lives
// under one of roots — a shim or agent helper that escaped its unit cgroup.
// The uninstaller itself may run from the installed binary, so it skips its
// own pid.
func killStrayProcesses(roots []string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	self := os.Getpid()
	killed := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			continue
		}
		exe = strings.TrimSuffix(exe, " (deleted)")
		if !pathUnderAny(exe, roots) {
			continue
		}
		if dryRun {
			planned("kill pid %d (%s)", pid, exe)
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			info("killed stray process %d (%s)", pid, exe)
			killed++
		}
	}
	return killed, nil
}

// teardownNetworking removes the machine-local virtual network state the agent
// built: container netns entries, workload veths, the WireGuard tunnel link,
// the routes it owns, and its nftables tables. Every step is best effort —
// a leftover here is harmless to the host and is reported, not fatal.
func teardownNetworking() {
	// Deleting a named netns unmounts /run/netns/<id> and destroys the
	// container-side veth end, which takes the host end with it.
	if entries, err := os.ReadDir(netnsRunDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, containerIDPrefix) {
				continue
			}
			if dryRun {
				planned("delete netns %s", name)
				continue
			}
			if err := netns.DeleteNamed(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				warn("deleting netns %s: %v", name, err)
				continue
			}
			info("deleted netns %s", name)
		}
	}

	if links, err := netlink.LinkList(); err != nil {
		warn("listing links: %v", err)
	} else {
		for _, link := range links {
			name := link.Attrs().Name
			if name != network.WGLinkName && !network.IsHostVethName(name) {
				continue
			}
			if dryRun {
				planned("delete link %s", name)
				continue
			}
			var notFound netlink.LinkNotFoundError
			if err := netlink.LinkDel(link); err != nil && !errors.As(err, &notFound) {
				warn("deleting link %s: %v", name, err)
				continue
			}
			info("deleted link %s", name)
		}
	}

	filter := &netlink.Route{Protocol: network.AuditRouteProtocol(), Table: unix.RT_TABLE_MAIN}
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		routes, err := netlink.RouteListFiltered(family, filter, netlink.RT_FILTER_PROTOCOL|netlink.RT_FILTER_TABLE)
		if err != nil {
			warn("listing routes: %v", err)
			continue
		}
		for i := range routes {
			route := routes[i]
			if dryRun {
				planned("delete route %s", route.String())
				continue
			}
			if err := netlink.RouteDel(&route); err != nil && !errors.Is(err, unix.ESRCH) {
				warn("deleting route %s: %v", route.String(), err)
				continue
			}
			info("deleted route %s", route.String())
		}
	}

	conn, err := network.NewNftConn()
	if err != nil {
		warn("opening nftables: %v", err)
		return
	}
	for _, family := range []nftables.TableFamily{nftables.TableFamilyIPv4, nftables.TableFamilyIPv6} {
		tables, err := conn.ListTablesOfFamily(family)
		if err != nil {
			info("listing %s nftables tables: %v (skipping)", nftFamilyName(family), err)
			continue
		}
		for _, table := range tables {
			if table.Name != network.NftTableName {
				continue
			}
			if dryRun {
				planned("delete nftables table %s %s", nftFamilyName(family), table.Name)
				continue
			}
			conn.DelTable(table)
			if err := conn.Flush(); err != nil {
				warn("deleting nftables table %s %s: %v", nftFamilyName(family), table.Name, err)
				continue
			}
			info("deleted nftables table %s %s", nftFamilyName(family), table.Name)
		}
	}
}

func nftFamilyName(family nftables.TableFamily) string {
	if family == nftables.TableFamilyIPv6 {
		return "ip6"
	}
	return "ip"
}

// unmountUnder lazily unmounts everything mounted at or below root (container
// rootfs overlays and the bind mounts inside them), then verifies nothing is
// left. Removing a directory that still has a bind mount in it would recurse
// into the mounted host path, so a failure here must stop the caller.
func unmountUnder(root string) error {
	points, err := mountPointsUnderRoot(root)
	if err != nil {
		return err
	}
	for _, point := range points {
		if dryRun {
			planned("umount -l %s", point)
			continue
		}
		if err := unix.Unmount(point, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("unmounting %s: %w", point, err)
		}
		info("unmounted %s", point)
	}
	if dryRun {
		return nil
	}
	if left, err := mountPointsUnderRoot(root); err != nil {
		return err
	} else if len(left) > 0 {
		return fmt.Errorf("%s still has mounts (%s); refusing to delete it", root, strings.Join(left, ", "))
	}
	return nil
}

func mountPointsUnderRoot(root string) ([]string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("reading mountinfo: %w", err)
	}
	return mountPointsUnder(string(data), root), nil
}
