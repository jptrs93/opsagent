package assetstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

const InlineThresholdBytes = 10 * 1024 * 1024

var ErrLargeAssetS3Config = errors.New("large asset S3 settings are not configured")
var ErrAssetS3ConfigChangeRequiresLocal = errors.New("large asset S3 configuration cannot change while S3 assets or pending uploads exist")

type Store struct {
	DB                   *sqlite.PrimaryStorage
	Config               func() *apigen.ClusterSettings
	Loader               config.Loader
	Secrets              secretStore
	MigrationWake        <-chan struct{}
	BeforeLocalMigration func(context.Context) error

	mu sync.Mutex
}

type secretStore interface {
	RevealByID(id int32) ([]byte, error)
}

func (s *Store) AssetOperationLocker() sync.Locker {
	return &s.mu
}

func (s *Store) ValidateSettingsUpdate(current, next apigen.ClusterSettings) error {
	if s.effectiveS3Identity(current) == s.effectiveS3Identity(next) {
		return nil
	}
	for _, asset := range s.DB.ListAllAssetVersionsIncludingPending() {
		if strings.HasPrefix(asset.Location, "s3://") || strings.HasPrefix(asset.Location, "pending://") {
			return ErrAssetS3ConfigChangeRequiresLocal
		}
	}
	return nil
}

type s3Identity struct {
	separate    bool
	accessKeyID string
	secretID    int32
	bucket      string
	path        string
	region      string
	endpoint    string
}

func (s *Store) effectiveS3Identity(settings apigen.ClusterSettings) s3Identity {
	separate := s.Loader.MustLoadConfigBoolValue(settings.LargeAssets.UseSeparateS3)
	accessKeyID := settings.Backup.S3AccessKeyID
	secret := settings.Backup.S3SecretAccessKey
	bucket := settings.Backup.S3Bucket
	region := settings.Backup.S3Region
	endpoint := settings.Backup.S3Endpoint
	if separate {
		accessKeyID = settings.LargeAssets.S3AccessKeyID
		secret = settings.LargeAssets.S3SecretAccessKey
		bucket = settings.LargeAssets.S3Bucket
		region = settings.LargeAssets.S3Region
		endpoint = settings.LargeAssets.S3Endpoint
	}
	return s3Identity{
		separate:    separate,
		accessKeyID: s.Loader.MustLoadConfigStringValue(accessKeyID),
		secretID:    secret.ID,
		bucket:      s.Loader.MustLoadConfigStringValue(bucket),
		path:        s.Loader.MustLoadConfigStringValue(settings.LargeAssets.S3Path),
		region:      s.Loader.MustLoadConfigStringValue(region),
		endpoint:    s.Loader.MustLoadConfigStringValue(endpoint),
	}
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
		asset := s.DB.SetAsset(key, format, blob, spaceID)
		s.DB.NotifyAssetUpdate(asset)
		return asset, nil
	}
	return s.setLargeAssetFromReader(ctx, key, format, sizeBytes, r, spaceID)
}

func (s *Store) setLargeAssetFromReader(ctx context.Context, key, format string, sizeBytes int64, r io.Reader, spaceID int32) (*apigen.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.Config()
	tmp, err := os.CreateTemp(ainit.StaticConfig.LargeAssetsDir, ".upload-*")
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
	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("sync staged large asset upload: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("stage large asset upload: %w", err)
	}

	asset := s.DB.SetAssetStored(key, format, pendingLocation(filepath.Base(tmp.Name())), sizeBytes, []byte{}, spaceID)
	var location string
	if s.Loader.MustLoadConfigBoolValue(cfg.Backup.Enabled) {
		client, bucket, err := s.s3Client(cfg)
		if err != nil {
			s.DB.DeleteAssetVersionByID(asset.ID)
			return nil, err
		}
		prefix := s.Loader.MustLoadConfigStringValue(cfg.LargeAssets.S3Path)
		s3ObjectKey := objectKey(prefix, asset.ID)
		location = "s3://" + bucket + "/" + s3ObjectKey
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        aws.String(bucket),
			Key:           aws.String(s3ObjectKey),
			Body:          tmp,
			ContentLength: aws.Int64(sizeBytes),
		}); err != nil {
			s.DB.DeleteAssetVersionByID(asset.ID)
			return nil, fmt.Errorf("write large asset to s3: %w", err)
		}
	} else {
		location = localLocation(asset.ID)
		if err := tmp.Close(); err != nil {
			s.DB.DeleteAssetVersionByID(asset.ID)
			return nil, fmt.Errorf("close large asset: %w", err)
		}
		if err := os.Rename(tmp.Name(), localPath(asset.ID)); err != nil {
			s.DB.DeleteAssetVersionByID(asset.ID)
			return nil, fmt.Errorf("store large asset locally: %w", err)
		}
		if err := syncDir(ainit.StaticConfig.LargeAssetsDir); err != nil {
			s.DB.DeleteAssetVersionByID(asset.ID)
			return nil, fmt.Errorf("sync large asset directory: %w", err)
		}
	}
	if location == "" {
		s.DB.DeleteAssetVersionByID(asset.ID)
		return nil, fmt.Errorf("large asset location was not selected")
	}
	asset = s.DB.UpdateAssetLocation(asset.ID, location)
	asset.Blob = nil
	s.DB.NotifyAssetUpdate(asset)
	return asset, nil
}

func (s *Store) OpenAsset(ctx context.Context, assetID int32) (*apigen.Asset, io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.DB.GetAssetByID(assetID)
	if !ok {
		return nil, nil, fmt.Errorf("asset %d not found", assetID)
	}
	if strings.HasPrefix(asset.Location, "s3://") {
		body, err := s.openS3Asset(ctx, asset.Location)
		if err != nil {
			return nil, nil, err
		}
		return asset, body, nil
	}
	if strings.HasPrefix(asset.Location, "local://") {
		id, err := parseLocalLocation(asset.Location)
		if err != nil {
			return nil, nil, err
		}
		body, err := os.Open(localPath(id))
		if err != nil {
			return nil, nil, fmt.Errorf("read local large asset: %w", err)
		}
		return asset, body, nil
	}
	if asset.Location != "" {
		return nil, nil, fmt.Errorf("unsupported asset location %q", asset.Location)
	}
	return asset, io.NopCloser(bytes.NewReader(asset.Blob)), nil
}

func (s *Store) DeleteAsset(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	latest, ok := s.DB.GetAsset(key, 0)
	versions := s.DB.ListAssetVersionsByKeyIncludingPending(key)
	s.DB.DeleteAsset(key)
	if ok {
		s.DB.NotifyAssetDeleted(latest)
	}
	for _, asset := range versions {
		if strings.HasPrefix(asset.Location, "local://") {
			id, err := parseLocalLocation(asset.Location)
			if err != nil {
				return err
			}
			if err := os.Remove(localPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("delete local large asset: %w", err)
			}
		}
		if strings.HasPrefix(asset.Location, "pending://") {
			name, err := parsePendingLocation(asset.Location)
			if err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(ainit.StaticConfig.LargeAssetsDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("delete staged large asset: %w", err)
			}
		}
	}
	return nil
}

func (s *Store) RenameAsset(ctx context.Context, oldKey, newKey string) (*apigen.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	asset, err := s.DB.RenameAsset(oldKey, newKey)
	if err != nil {
		return nil, err
	}
	versions := s.DB.ListAssetVersionsByKey(newKey)
	for _, version := range versions {
		s.DB.NotifyAssetUpdate(version)
	}
	return asset, nil
}

func (s *Store) openS3Asset(ctx context.Context, location string) (io.ReadCloser, error) {
	bucket, key, err := parseS3Location(location)
	if err != nil {
		return nil, err
	}
	client, _, err := s.s3Client(s.Config())
	if err != nil {
		return nil, err
	}
	res, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("read large asset from s3: %w", err)
	}
	return res.Body, nil
}

func (s *Store) s3Client(cfg *apigen.ClusterSettings) (*s3.Client, string, error) {
	separate := s.Loader.MustLoadConfigBoolValue(cfg.LargeAssets.UseSeparateS3)
	accessKeySetting := cfg.Backup.S3AccessKeyID
	secretRef := cfg.Backup.S3SecretAccessKey
	bucketSetting := cfg.Backup.S3Bucket
	regionSetting := cfg.Backup.S3Region
	endpointSetting := cfg.Backup.S3Endpoint
	if separate {
		accessKeySetting = cfg.LargeAssets.S3AccessKeyID
		secretRef = cfg.LargeAssets.S3SecretAccessKey
		bucketSetting = cfg.LargeAssets.S3Bucket
		regionSetting = cfg.LargeAssets.S3Region
		endpointSetting = cfg.LargeAssets.S3Endpoint
	}
	secretAccessKey, err := revealSecretRef(s.Secrets, secretRef)
	if err != nil {
		return nil, "", fmt.Errorf("reveal large asset S3 secret access key: %w", err)
	}
	accessKeyID := s.Loader.MustLoadConfigStringValue(accessKeySetting)
	bucket := s.Loader.MustLoadConfigStringValue(bucketSetting)
	region := s.Loader.MustLoadConfigStringValue(regionSetting)
	endpoint := s.Loader.MustLoadConfigStringValue(endpointSetting)
	if accessKeyID == "" || secretAccessKey == "" || bucket == "" || region == "" {
		return nil, "", fmt.Errorf("%w: access key ID, secret access key, bucket, and region are required", ErrLargeAssetS3Config)
	}
	awsCfg := aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})
	return client, bucket, nil
}

func revealSecretRef(secrets secretStore, ref apigen.SecretRef) (string, error) {
	if ref.ID == 0 {
		return "", nil
	}
	value, err := secrets.RevealByID(ref.ID)
	if err != nil {
		return "", err
	}
	return string(value), nil
}
