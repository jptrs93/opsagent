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

func (p *PrimaryAssetProvider) OpenAsset(ctx context.Context, assetVersionID int32) (io.ReadCloser, error) {
	params := url.Values{}
	params.Set("asset_version_id", strconv.Itoa(int(assetVersionID)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/cluster/asset?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching asset from primary: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, clusterAssetHTTPError(resp)
	}
	return resp.Body, nil
}

func clusterAssetHTTPError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(body) > 0 {
		apiErr, err := apigen.DecodeApiErr(body)
		if err == nil && apiErr != nil {
			return apiErr
		}
	}
	return fmt.Errorf("asset download HTTP %d", resp.StatusCode)
}
