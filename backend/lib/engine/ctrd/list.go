package ctrd

import (
	"context"
	"fmt"

	containerd "github.com/containerd/containerd/v2/client"
)

// ContainerState is one container in the namespace and whether its task is
// currently running.
type ContainerState struct {
	ID          string
	TaskRunning bool
}

// ListContainerStates returns every container in the namespace with its task
// state. A container whose task is gone or has exited reports TaskRunning
// false.
func (c *Client) ListContainerStates(ctx context.Context) ([]ContainerState, error) {
	cl, err := c.ensure()
	if err != nil {
		return nil, err
	}
	ctx = c.withNS(ctx)
	containers, err := cl.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}
	out := make([]ContainerState, 0, len(containers))
	for _, container := range containers {
		st := ContainerState{ID: container.ID()}
		if task, err := container.Task(ctx, nil); err == nil {
			if status, err := task.Status(ctx); err == nil && status.Status == containerd.Running {
				st.TaskRunning = true
			}
		}
		out = append(out, st)
	}
	return out, nil
}
