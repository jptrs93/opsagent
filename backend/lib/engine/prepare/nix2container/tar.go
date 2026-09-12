// Ported from github.com/nlewo/nix2container (Apache License 2.0); the copy is
// documented in LICENSE-nix2container in this directory.
package nix2container

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

type fileNode struct {
	srcPath   string
	matchPath string
	info      *os.FileInfo
	options   *PathOptions
	contents  map[string]*fileNode
}

func newFileNode() *fileNode {
	return &fileNode{contents: make(map[string]*fileNode)}
}

func (store Store) LayerTar(paths []Path) io.ReadCloser {
	r, w := io.Pipe()
	tw := tar.NewWriter(w)
	graph := newFileNode()
	go func() {
		defer w.Close()
		for _, p := range paths {
			hostRoot, err := store.HostPath(p.Path)
			if err != nil {
				_ = w.CloseWithError(err)
				return
			}
			options := p.Options
			err = filepath.Walk(hostRoot, func(hostPath string, info os.FileInfo, err error) error {
				if err != nil {
					return fmt.Errorf("failed accessing path %q: %v", hostPath, err)
				}
				canonical, err := store.canonicalPath(hostPath)
				if err != nil {
					return err
				}
				return addFileToGraph(graph, hostPath, canonical, &info, options)
			})
			if err != nil {
				_ = w.CloseWithError(err)
				return
			}
		}
		err := walkGraph(graph, func(node *fileNode, dstPath string) error {
			if node.info == nil {
				return createDirectory(tw, dstPath)
			}
			return appendFileToTar(tw, node.srcPath, node.matchPath, dstPath, *node.info, node.options)
		})
		if err != nil {
			_ = w.CloseWithError(err)
			return
		}
		if err := tw.Close(); err != nil {
			_ = w.CloseWithError(err)
		}
	}()
	return r
}

func tarPath(canonical string, options *PathOptions) string {
	if options != nil && options.Rewrite.Regex != "" {
		re := regexp.MustCompile(options.Rewrite.Regex)
		return string(re.ReplaceAll([]byte(canonical), []byte(options.Rewrite.Repl)))
	}
	return canonical
}

func splitPath(p string) []string {
	cleaned := filepath.Clean(p)
	parts := strings.Split(cleaned, "/")
	if len(parts) == 2 && parts[0] == "" && parts[1] == "" {
		return []string{""}
	}
	return parts
}

func addFileToGraph(root *fileNode, hostPath, canonical string, info *os.FileInfo, options *PathOptions) error {
	dstPath := tarPath(canonical, options)
	if dstPath == "" {
		return nil
	}
	current := root
	for _, part := range splitPath(dstPath) {
		node, exists := current.contents[part]
		if !exists {
			node = newFileNode()
			current.contents[part] = node
		}
		current = node
	}
	if current.info != nil {
		if (*current.info).Mode() != (*info).Mode() {
			return fmt.Errorf("the file '%s' already exists in the graph with mode '%v' from '%s' while it is added again with mode '%v' by '%s'",
				dstPath, (*current.info).Mode(), current.matchPath, (*info).Mode(), canonical)
		}
		if (*current.info).Mode().IsRegular() && (*current.info).Size() != (*info).Size() {
			return fmt.Errorf("the file '%s' already exists in the graph with size '%d' from '%s' while it is added again with size '%d' by '%s'",
				dstPath, (*current.info).Size(), current.matchPath, (*info).Size(), canonical)
		}
	}
	current.info = info
	if current.options != nil && (options == nil || !reflect.DeepEqual(current.options.Perms, options.Perms)) {
		return fmt.Errorf("the file '%s' already exists in the tar with perms %#v but is overridden", dstPath, current.options.Perms)
	}
	current.options = options
	current.srcPath = hostPath
	current.matchPath = canonical
	return nil
}

func walkGraph(root *fileNode, fn func(node *fileNode, dstPath string) error) error {
	return walkGraphFn("", root, fn)
}

func walkGraphFn(base string, root *fileNode, fn func(node *fileNode, dstPath string) error) error {
	keys := make([]string, 0, len(root.contents))
	for k := range root.contents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		dstPath := filepath.Join(base, k)
		if k == "" {
			dstPath = filepath.Join("/", k)
		}
		if err := fn(root.contents[k], dstPath); err != nil {
			return err
		}
		if err := walkGraphFn(dstPath, root.contents[k], fn); err != nil {
			return err
		}
	}
	return nil
}

var (
	tarEpoch    = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	tarModTime  = time.Date(1970, 1, 1, 0, 0, 1, 0, time.UTC)
	tarRootName = "root"
)

func createDirectory(tw *tar.Writer, dstPath string) error {
	hdr := &tar.Header{
		Name:       dstPath,
		Typeflag:   tar.TypeDir,
		Uid:        0,
		Gid:        0,
		Uname:      tarRootName,
		Gname:      tarRootName,
		ModTime:    tarModTime,
		AccessTime: tarEpoch,
		ChangeTime: tarEpoch,
		Mode:       0o755,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("could not write hdr '%#v', got error '%s'", hdr, err.Error())
	}
	return nil
}

func appendFileToTar(tw *tar.Writer, srcPath, matchPath, dstPath string, info os.FileInfo, opts *PathOptions) error {
	var link string
	var err error
	if info.Mode()&os.ModeSymlink != 0 {
		link, err = os.Readlink(srcPath)
		if err != nil {
			return err
		}
	}
	hdr, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	hdr.Name = dstPath
	hdr.Uid = 0
	hdr.Gid = 0
	hdr.Uname = tarRootName
	hdr.Gname = tarRootName
	if link != "" {
		hdr.Mode = 0o777
	}
	if opts != nil {
		for _, perms := range opts.Perms {
			re := regexp.MustCompile(perms.Regex)
			if re.Match([]byte(matchPath)) {
				hdr.Uid = perms.Uid
				hdr.Gid = perms.Gid
				if perms.Uname != "" {
					hdr.Uname = perms.Uname
				}
				if perms.Gname != "" {
					hdr.Gname = perms.Gname
				}
				if perms.Mode != "" {
					if _, err := fmt.Sscanf(perms.Mode, "%o", &hdr.Mode); err != nil {
						return err
					}
				}
			}
		}
	}
	hdr.ModTime = tarModTime
	hdr.AccessTime = tarEpoch
	hdr.ChangeTime = tarEpoch
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("could not write hdr '%#v', got error '%s'", hdr, err.Error())
	}
	if link != "" || info.IsDir() {
		return nil
	}
	file, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("could not open file '%s', got error '%s'", srcPath, err.Error())
	}
	defer file.Close()
	if _, err := io.Copy(tw, file); err != nil {
		return fmt.Errorf("could not copy the file '%s' data to the tarball, got error '%s'", srcPath, err.Error())
	}
	return nil
}

func (store Store) LayerBlob(layer Layer) (io.ReadCloser, error) {
	if layer.LayerPath != "" {
		hostPath, err := store.HostPath(layer.LayerPath)
		if err != nil {
			return nil, err
		}
		return os.Open(hostPath)
	}
	return store.LayerTar(layer.Paths), nil
}
