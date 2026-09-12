package nix2container

import (
	_ "crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	ImageVersion      = 1
	CanonicalStoreDir = "/nix/store"
	canonicalRoot     = "/nix"
	MediaTypeLayer    = ocispec.MediaTypeImageLayer
)

var ErrNotImage = errors.New("output is not a nix2container image")

type Image struct {
	Version     int                 `json:"version"`
	ImageConfig ocispec.ImageConfig `json:"image-config"`
	Layers      []Layer             `json:"layers"`
	Arch        string              `json:"arch"`
	Created     *time.Time          `json:"created"`
}

type Layer struct {
	Digest    string          `json:"digest"`
	Size      int64           `json:"size"`
	DiffIDs   string          `json:"diff_ids"`
	Paths     []Path          `json:"paths,omitempty"`
	MediaType string          `json:"mediatype"`
	LayerPath string          `json:"layer-path,omitempty"`
	History   ocispec.History `json:"History"`
}

type Path struct {
	Path    string       `json:"path"`
	Options *PathOptions `json:"options,omitempty"`
}

type PathOptions struct {
	Rewrite Rewrite `json:"rewrite,omitempty"`
	Perms   []Perm  `json:"perms,omitempty"`
}

type Rewrite struct {
	Regex string `json:"regex"`
	Repl  string `json:"repl"`
}

type Perm struct {
	Regex string `json:"regex"`
	Mode  string `json:"mode"`
	Uid   int    `json:"uid"`
	Gid   int    `json:"gid"`
	Uname string `json:"uname"`
	Gname string `json:"gname"`
}

type Store struct {
	Root string
}

func (s Store) HostPath(canonical string) (string, error) {
	clean := path.Clean(canonical)
	if clean != canonicalRoot && !strings.HasPrefix(clean, canonicalRoot+"/") {
		return "", fmt.Errorf("path %q is outside %s", canonical, canonicalRoot)
	}
	return filepath.Join(s.Root, filepath.FromSlash(strings.TrimPrefix(clean, canonicalRoot))), nil
}

func (s Store) canonicalPath(hostPath string) (string, error) {
	rel, err := filepath.Rel(s.Root, hostPath)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return canonicalRoot, nil
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path %q is outside store root %s", hostPath, s.Root)
	}
	return path.Join(canonicalRoot, filepath.ToSlash(rel)), nil
}

var storePathPattern = regexp.MustCompile(`^[0-9a-df-np-sv-z]{32}-[^/]+$`)

func IsStorePath(canonical string) bool {
	clean := path.Clean(canonical)
	dir, base := path.Split(clean)
	return path.Clean(dir) == CanonicalStoreDir && storePathPattern.MatchString(base)
}

func Load(store Store, canonicalJSONPath string) (*Image, error) {
	if !IsStorePath(canonicalJSONPath) {
		return nil, fmt.Errorf("%w: %s is not a store path", ErrNotImage, canonicalJSONPath)
	}
	hostPath, err := store.HostPath(canonicalJSONPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotImage, err)
	}
	info, err := os.Lstat(hostPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotImage, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrNotImage, canonicalJSONPath)
	}
	if info.Mode()&0o111 != 0 {
		return nil, fmt.Errorf("%w: %s is executable", ErrNotImage, canonicalJSONPath)
	}
	if !strings.HasSuffix(canonicalJSONPath, ".json") {
		return nil, fmt.Errorf("%w: %s does not end in .json", ErrNotImage, canonicalJSONPath)
	}
	raw, err := os.ReadFile(hostPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotImage, err)
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Image, error) {
	var image Image
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&image); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotImage, err)
	}
	if err := image.Validate(); err != nil {
		return nil, err
	}
	return &image, nil
}

func (image *Image) Validate() error {
	if image.Version != ImageVersion {
		return fmt.Errorf("%w: unsupported image version %d", ErrNotImage, image.Version)
	}
	if len(image.Layers) == 0 {
		return fmt.Errorf("%w: image has no layers", ErrNotImage)
	}
	if image.Arch == "" {
		return fmt.Errorf("%w: image has no architecture", ErrNotImage)
	}
	for i, layer := range image.Layers {
		if err := layer.validate(); err != nil {
			return fmt.Errorf("%w: layer %d: %v", ErrNotImage, i, err)
		}
	}
	return nil
}

func (layer Layer) validate() error {
	if layer.MediaType != MediaTypeLayer {
		return fmt.Errorf("media type %q is not %s", layer.MediaType, MediaTypeLayer)
	}
	if _, err := digest.Parse(layer.Digest); err != nil {
		return fmt.Errorf("digest %q: %v", layer.Digest, err)
	}
	if layer.DiffIDs != layer.Digest {
		return fmt.Errorf("diff id %q does not match digest %q of an uncompressed layer", layer.DiffIDs, layer.Digest)
	}
	if layer.Size <= 0 {
		return fmt.Errorf("size %d is not positive", layer.Size)
	}
	if layer.LayerPath != "" {
		if len(layer.Paths) != 0 {
			return fmt.Errorf("layer names both a layer path and store paths")
		}
		if !IsStorePath(layer.LayerPath) && !strings.HasPrefix(layer.LayerPath, CanonicalStoreDir+"/") {
			return fmt.Errorf("layer path %q is not under %s", layer.LayerPath, CanonicalStoreDir)
		}
		return nil
	}
	if len(layer.Paths) == 0 {
		return fmt.Errorf("layer has neither store paths nor a layer path")
	}
	for _, p := range layer.Paths {
		if !IsStorePath(p.Path) {
			return fmt.Errorf("path %q is not a store path", p.Path)
		}
		if p.Options != nil {
			if p.Options.Rewrite.Regex != "" {
				if _, err := regexp.Compile(p.Options.Rewrite.Regex); err != nil {
					return fmt.Errorf("rewrite regex for %s: %v", p.Path, err)
				}
			}
			for _, perm := range p.Options.Perms {
				if _, err := regexp.Compile(perm.Regex); err != nil {
					return fmt.Errorf("perm regex for %s: %v", p.Path, err)
				}
			}
		}
	}
	return nil
}

func (layer Layer) ParsedDigest() digest.Digest {
	return digest.Digest(layer.Digest)
}

func (image *Image) Config(diffIDs []digest.Digest) ocispec.Image {
	config := ocispec.Image{
		Platform: ocispec.Platform{OS: "linux", Architecture: image.Arch},
		Config:   image.ImageConfig,
		Created:  image.Created,
		RootFS:   ocispec.RootFS{Type: "layers", DiffIDs: diffIDs},
	}
	for _, layer := range image.Layers {
		config.History = append(config.History, layer.History)
	}
	return config
}
