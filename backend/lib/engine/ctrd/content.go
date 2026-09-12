package ctrd

import (
	"context"
	"fmt"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ContentSession is a leased view of the containerd content store for one
// image ingest. Content written under the lease is protected from garbage
// collection until Done is called, by which time the image referencing it
// exists.
type ContentSession struct {
	Ctx   context.Context
	Store content.Store
	done  func(context.Context) error
}

func (c *Client) OpenContentSession(ctx context.Context) (*ContentSession, error) {
	cl, err := c.ensure()
	if err != nil {
		return nil, err
	}
	ctx = c.withNS(ctx)
	leased, done, err := cl.WithLease(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating content lease: %w", err)
	}
	return &ContentSession{Ctx: leased, Store: cl.ContentStore(), done: done}, nil
}

func (s *ContentSession) Done() {
	_ = s.done(context.WithoutCancel(s.Ctx))
}

// TagManifest points ref at an image manifest already present in the content
// store and unpacks it into the default snapshotter.
func (c *Client) TagManifest(ctx context.Context, ref string, manifest ocispec.Descriptor) error {
	cl, err := c.ensure()
	if err != nil {
		return err
	}
	ctx = c.withNS(ctx)
	record := images.Image{Name: ref, Target: manifest}
	store := cl.ImageService()
	if _, err := store.Create(ctx, record); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return fmt.Errorf("creating image %s: %w", ref, err)
		}
		if _, err := store.Update(ctx, record); err != nil {
			return fmt.Errorf("updating image %s: %w", ref, err)
		}
	}
	img := containerd.NewImage(cl, record)
	if err := img.Unpack(ctx, ""); err != nil {
		return fmt.Errorf("unpacking image %s: %w", ref, err)
	}
	return nil
}

// EnsureImage pulls ref from its registry unless it is already present and
// unpacked, and returns the image's target digest and whether a pull ran.
func (c *Client) EnsureImage(ctx context.Context, ref string) (string, bool, error) {
	cl, err := c.ensure()
	if err != nil {
		return "", false, err
	}
	nsCtx := c.withNS(ctx)
	img, err := cl.GetImage(nsCtx, ref)
	if err == nil {
		if unpacked, err := img.IsUnpacked(nsCtx, ""); err == nil && unpacked {
			return img.Target().Digest.String(), false, nil
		}
	} else if !errdefs.IsNotFound(err) {
		return "", false, fmt.Errorf("loading image %q: %w", ref, err)
	}
	if _, err := c.Pull(ctx, ref, nil); err != nil {
		return "", false, fmt.Errorf("%w: %s: %v", ErrImagePull, ref, err)
	}
	img, err = cl.GetImage(nsCtx, ref)
	if err != nil {
		return "", false, fmt.Errorf("loading pulled image %q: %w", ref, err)
	}
	return img.Target().Digest.String(), true, nil
}
