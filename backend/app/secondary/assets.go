package secondary

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type PrimaryAssetProvider struct {
	baseURL    string
	httpClient *http.Client
}

func NewPrimaryAssetProvider(baseURL string, client *http.Client) *PrimaryAssetProvider {
	if client == nil {
		client = http.DefaultClient
	}
	return &PrimaryAssetProvider{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: client,
	}
}

func (p *PrimaryAssetProvider) OpenAsset(ctx context.Context, assetID int32) (*apigen.Asset, io.ReadCloser, error) {
	params := url.Values{}
	params.Set("asset_id", strconv.Itoa(int(assetID)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/cluster/asset?"+params.Encode(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching asset from primary: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, nil, clusterAssetHTTPError(resp)
	}
	asset := &apigen.Asset{
		ID:        int32Header(resp.Header, "X-Opsagent-Asset-ID", assetID),
		Key:       assetKeyHeader(resp.Header),
		Version:   int32Header(resp.Header, "X-Opsagent-Asset-Version", 0),
		Format:    resp.Header.Get("X-Opsagent-Asset-Format"),
		SizeBytes: contentLengthInt32(resp.ContentLength),
	}
	return asset, resp.Body, nil
}

func clusterAssetHTTPError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(body) > 0 {
		apiErr, decodeErr := apigen.DecodeApiErr(body)
		if decodeErr == nil && apiErr != nil {
			return apiErr
		}
	}
	return fmt.Errorf("asset download HTTP %d", resp.StatusCode)
}

func int32Header(header http.Header, key string, fallback int32) int32 {
	value, err := strconv.ParseInt(header.Get(key), 10, 32)
	if err != nil || value <= 0 {
		return fallback
	}
	return int32(value)
}

func assetKeyHeader(header http.Header) string {
	raw := header.Get("X-Opsagent-Asset-Key")
	key, err := url.QueryUnescape(raw)
	if err != nil {
		return raw
	}
	return key
}

func contentLengthInt32(contentLength int64) int32 {
	if contentLength <= 0 || contentLength > int64(^uint32(0)>>1) {
		return 0
	}
	return int32(contentLength)
}
