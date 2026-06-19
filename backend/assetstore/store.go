package assetstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

const InlineThresholdBytes = 10 * 1024 * 1024

var ErrLargeAssetS3Config = errors.New("large asset S3 settings are not configured")

type Store struct {
	DB     *sqlite.PrimaryStorage
	Config func() ainit.DynamicConfiguration
}

func (s *Store) GetAssetForPreview(key string, version int32) (*apigen.Asset, bool, error) {
	asset, ok := s.DB.GetAsset(key, version)
	if !ok || asset.Location == "" {
		return asset, ok, nil
	}
	asset.Blob = nil
	return asset, true, nil
}

func (s *Store) SetAsset(ctx context.Context, key, format string, blob []byte, spaceID int32) (*apigen.Asset, error) {
	return s.SetAssetFromReader(ctx, key, format, int64(len(blob)), bytes.NewReader(blob), spaceID)
}

func (s *Store) SetAssetFromReader(ctx context.Context, key, format string, sizeBytes int64, r io.Reader, spaceID int32) (*apigen.Asset, error) {
	if sizeBytes < 0 {
		return nil, fmt.Errorf("asset upload requires a content length")
	}
	if sizeBytes > math.MaxInt32 {
		return nil, fmt.Errorf("asset upload is too large: maximum supported size is %d bytes", math.MaxInt32)
	}
	if sizeBytes <= InlineThresholdBytes {
		blob, err := io.ReadAll(io.LimitReader(r, InlineThresholdBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read inline asset upload: %w", err)
		}
		if int64(len(blob)) != sizeBytes {
			return nil, fmt.Errorf("asset upload size changed while reading")
		}
		return s.DB.SetAsset(key, format, blob, spaceID), nil
	}
	return s.setLargeAssetFromReader(ctx, key, format, sizeBytes, r, spaceID)
}

func (s *Store) setLargeAssetFromReader(ctx context.Context, key, format string, sizeBytes int64, r io.Reader, spaceID int32) (*apigen.Asset, error) {
	cfg := s.Config()
	client, bucket, err := s.s3Client(cfg, true)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "opendeploy-large-asset-*")
	if err != nil {
		return nil, fmt.Errorf("stage large asset upload: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if written, err := io.Copy(tmp, r); err != nil {
		return nil, fmt.Errorf("stage large asset upload: %w", err)
	} else if written != sizeBytes {
		return nil, fmt.Errorf("asset upload size changed while reading")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("stage large asset upload: %w", err)
	}

	asset := s.DB.SetAssetStored(key, format, "", sizeBytes, []byte{}, spaceID)
	objectKey := objectKey(cfg.LargeAssetS3Path, asset.ID)
	location := "s3://" + bucket + "/" + objectKey
	if _, err := client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(objectKey),
		Body:          tmp,
		ContentLength: aws.Int64(sizeBytes),
	}); err != nil {
		s.DB.DeleteAssetVersionByID(asset.ID)
		return nil, fmt.Errorf("write large asset to s3: %w", err)
	}
	asset = s.DB.UpdateAssetLocation(asset.ID, location)
	asset.Blob = nil
	return asset, nil
}

func (s *Store) OpenAsset(ctx context.Context, assetID, version int32) (*apigen.Asset, io.ReadCloser, error) {
	asset, ok := s.DB.GetAssetByIDVersion(assetID, version)
	if !ok {
		return nil, nil, fmt.Errorf("asset %d version %d not found", assetID, version)
	}
	if asset.Location != "" {
		body, err := s.openS3Asset(ctx, asset.Location)
		if err != nil {
			return nil, nil, err
		}
		return asset, body, nil
	}
	return asset, io.NopCloser(bytes.NewReader(asset.Blob)), nil
}

func (s *Store) DeleteAsset(ctx context.Context, key string) error {
	for _, asset := range s.DB.ListAssetVersionsByKey(key) {
		if asset.Location == "" {
			continue
		}
		if err := s.deleteS3Asset(ctx, asset.Location); err != nil {
			return err
		}
	}
	s.DB.DeleteAsset(key)
	return nil
}

func (s *Store) deleteS3Asset(ctx context.Context, location string) error {
	bucket, key, err := parseS3Location(location)
	if err != nil {
		return err
	}
	client, _, err := s.s3Client(s.Config(), true)
	if err != nil {
		return err
	}
	if _, err := client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("delete large asset from s3: %w", err)
	}
	return nil
}

func (s *Store) openS3Asset(ctx context.Context, location string) (io.ReadCloser, error) {
	bucket, key, err := parseS3Location(location)
	if err != nil {
		return nil, err
	}
	client, _, err := s.s3Client(s.Config(), true)
	if err != nil {
		return nil, err
	}
	res, err := client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("read large asset from s3: %w", err)
	}
	return res.Body, nil
}

func (s *Store) s3Client(cfg ainit.DynamicConfiguration, requireEnabled bool) (*awss3.Client, string, error) {
	if requireEnabled && !cfg.LargeAssetS3Enabled {
		return nil, "", fmt.Errorf("%w: large asset S3 settings must be enabled before storing or reading large assets", ErrLargeAssetS3Config)
	}
	secretAccessKey, err := revealSecretValue(cfg.LargeAssetS3SecretAccessKey)
	if err != nil {
		return nil, "", fmt.Errorf("reveal large asset S3 secret access key: %w", err)
	}
	if cfg.LargeAssetS3AccessKeyID == "" || secretAccessKey == "" || cfg.LargeAssetS3Bucket == "" || cfg.LargeAssetS3Region == "" {
		return nil, "", fmt.Errorf("%w: access key ID, secret access key, bucket, and region are required", ErrLargeAssetS3Config)
	}
	awsCfg := aws.Config{
		Region:      cfg.LargeAssetS3Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.LargeAssetS3AccessKeyID, secretAccessKey, ""),
	}
	client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		if cfg.LargeAssetS3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.LargeAssetS3Endpoint)
			o.UsePathStyle = true
		}
	})
	return client, cfg.LargeAssetS3Bucket, nil
}

func objectKey(prefix string, assetID int32) string {
	base := strconv.Itoa(int(assetID))
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return base
	}
	return prefix + "/" + base
}

func parseS3Location(location string) (string, string, error) {
	const scheme = "s3://"
	if !strings.HasPrefix(location, scheme) {
		return "", "", fmt.Errorf("unsupported asset location %q", location)
	}
	parts := strings.SplitN(strings.TrimPrefix(location, scheme), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid asset location %q", location)
	}
	return parts[0], parts[1], nil
}

func revealSecretValue(value interface {
	Reveal() (string, error)
}) (string, error) {
	if value == nil {
		return "", nil
	}
	return value.Reveal()
}
