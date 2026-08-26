package opendeployrelease

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"time"
)

func opendeployBinaryOCI(ref string, binary []byte) (io.Reader, error) {
	layer, err := opendeployImageLayer(binary)
	if err != nil {
		return nil, err
	}
	layerDigest := sha256Bytes(layer)

	config := map[string]any{
		"architecture": runtime.GOARCH,
		"os":           runtime.GOOS,
		"config": map[string]any{
			"Entrypoint": []string{"/opendeploy"},
		},
		"rootfs": map[string]any{
			"type":     "layers",
			"diff_ids": []string{"sha256:" + layerDigest},
		},
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	configDigest := sha256Bytes(configBytes)

	manifest := map[string]any{
		"schemaVersion": 2,
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    "sha256:" + configDigest,
			"size":      len(configBytes),
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.oci.image.layer.v1.tar",
			"digest":    "sha256:" + layerDigest,
			"size":      len(layer),
		}},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	manifestDigest := sha256Bytes(manifestBytes)

	index := map[string]any{
		"schemaVersion": 2,
		"manifests": []map[string]any{{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    "sha256:" + manifestDigest,
			"size":      len(manifestBytes),
			"annotations": map[string]string{
				"org.opencontainers.image.ref.name": ref,
			},
		}},
	}
	indexBytes, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	if err := writeTarFile(tw, "oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		return nil, err
	}
	if err := writeTarFile(tw, "index.json", indexBytes, 0o644); err != nil {
		return nil, err
	}
	for _, blob := range []struct {
		digest string
		data   []byte
	}{
		{configDigest, configBytes}, {manifestDigest, manifestBytes}, {layerDigest, layer},
	} {
		if err := writeTarFile(tw, "blobs/sha256/"+blob.digest, blob.data, 0o644); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(out.Bytes()), nil
}

func opendeployImageLayer(binary []byte) ([]byte, error) {
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	if err := writeTarFile(tw, "opendeploy", binary, 0o755); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeTarFile(tw *tar.Writer, name string, data []byte, mode int64) error {
	if err := tw.WriteHeader(&tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(data)),
		ModTime: time.Unix(0, 0),
	}); err != nil {
		return err
	}
	n, err := tw.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("short tar write for %s", name)
	}
	return nil
}

func sha256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
