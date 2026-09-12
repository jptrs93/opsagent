package nix2container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var ErrLayerMismatch = errors.New("image layer failed verification")

type LayerOutcome struct {
	Index  int
	Digest digest.Digest
	Size   int64
	Reused bool
}

type IngestResult struct {
	Manifest ocispec.Descriptor
	Layers   []LayerOutcome
}

type Logger func(format string, args ...any)

func Ingest(ctx context.Context, cs content.Store, store Store, image *Image, record *Record, log Logger) (*IngestResult, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	result := &IngestResult{}
	layerDescs := make([]ocispec.Descriptor, 0, len(image.Layers))
	diffIDs := make([]digest.Digest, 0, len(image.Layers))
	for i, layer := range image.Layers {
		claimed := layer.ParsedDigest()
		exists, err := blobExists(ctx, cs, claimed)
		if err != nil {
			return nil, err
		}
		key := ""
		if layer.LayerPath == "" {
			key = LayerKey(layer.Paths)
		}
		outcome := LayerOutcome{Index: i, Digest: claimed, Size: layer.Size}
		if exists && key != "" && record.Verified(key, claimed) {
			outcome.Reused = true
			log("layer %d/%d reused %s (%d paths, verified earlier)", i+1, len(image.Layers), claimed, len(layer.Paths))
		} else {
			if err := writeLayer(ctx, cs, store, layer, exists); err != nil {
				return nil, fmt.Errorf("layer %d/%d: %w", i+1, len(image.Layers), err)
			}
			if key != "" {
				record.Put(key, claimed)
			}
			if exists {
				log("layer %d/%d verified %s against the existing blob (%d paths)", i+1, len(image.Layers), claimed, len(layer.Paths))
			} else {
				log("layer %d/%d generated %s (%d paths, %d bytes)", i+1, len(image.Layers), claimed, len(layer.Paths), layer.Size)
			}
		}
		result.Layers = append(result.Layers, outcome)
		layerDescs = append(layerDescs, ocispec.Descriptor{MediaType: MediaTypeLayer, Digest: claimed, Size: layer.Size})
		diffIDs = append(diffIDs, claimed)
	}
	if err := record.Save(); err != nil {
		return nil, fmt.Errorf("saving verified layer record: %w", err)
	}
	configBlob, err := json.Marshal(image.Config(diffIDs))
	if err != nil {
		return nil, err
	}
	configDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromBytes(configBlob), Size: int64(len(configBlob))}
	if err := content.WriteBlob(ctx, cs, "nix2container-config-"+configDesc.Digest.Encoded(), bytes.NewReader(configBlob), configDesc); err != nil {
		return nil, fmt.Errorf("writing image config: %w", err)
	}
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    layerDescs,
	}
	manifestBlob, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(manifestBlob),
		Size:      int64(len(manifestBlob)),
		Platform:  &ocispec.Platform{OS: "linux", Architecture: image.Arch},
	}
	labels := map[string]string{"containerd.io/gc.ref.content.config": configDesc.Digest.String()}
	for i, desc := range layerDescs {
		labels[fmt.Sprintf("containerd.io/gc.ref.content.l.%d", i)] = desc.Digest.String()
	}
	if err := content.WriteBlob(ctx, cs, "nix2container-manifest-"+manifestDesc.Digest.Encoded(), bytes.NewReader(manifestBlob), manifestDesc, content.WithLabels(labels)); err != nil {
		return nil, fmt.Errorf("writing image manifest: %w", err)
	}
	result.Manifest = manifestDesc
	return result, nil
}

func blobExists(ctx context.Context, cs content.Store, dgst digest.Digest) (bool, error) {
	_, err := cs.Info(ctx, dgst)
	if err == nil {
		return true, nil
	}
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("checking blob %s: %w", dgst, err)
}

func writeLayer(ctx context.Context, cs content.Store, store Store, layer Layer, exists bool) error {
	claimed := layer.ParsedDigest()
	reader, err := store.LayerBlob(layer)
	if err != nil {
		return err
	}
	defer reader.Close()
	ref := "nix2container-layer-" + claimed.Encoded()
	var writer content.Writer
	if !exists {
		writer, err = content.OpenWriter(ctx, cs, content.WithRef(ref), content.WithDescriptor(ocispec.Descriptor{MediaType: MediaTypeLayer, Digest: claimed, Size: layer.Size}))
		if errdefs.IsAlreadyExists(err) {
			writer = nil
		} else if err != nil {
			return fmt.Errorf("opening content writer: %w", err)
		}
	}
	digester := digest.Canonical.Digester()
	var sink io.Writer = digester.Hash()
	if writer != nil {
		sink = io.MultiWriter(sink, writer)
	}
	size, copyErr := io.Copy(sink, reader)
	computed := digester.Digest()
	if copyErr != nil || computed != claimed || size != layer.Size {
		if writer != nil {
			_ = writer.Close()
			_ = cs.Abort(ctx, ref)
		}
		if copyErr != nil {
			return fmt.Errorf("generating layer %s: %w", claimed, copyErr)
		}
		return fmt.Errorf("%w: claimed %s (%d bytes), regenerated %s (%d bytes)", ErrLayerMismatch, claimed, layer.Size, computed, size)
	}
	if writer == nil {
		return nil
	}
	if err := writer.Commit(ctx, size, computed); err != nil {
		_ = writer.Close()
		if errdefs.IsAlreadyExists(err) {
			return nil
		}
		_ = cs.Abort(ctx, ref)
		return fmt.Errorf("committing layer %s: %w", claimed, err)
	}
	return writer.Close()
}
