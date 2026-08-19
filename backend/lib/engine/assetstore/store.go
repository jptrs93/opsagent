package assetstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

const InlineThresholdBytes = 10 * 1024 * 1024

var ErrLargeAssetS3Config = errors.New("large asset S3 settings are not configured")
var ErrAssetS3ConfigChangeRequiresLocal = errors.New("large asset S3 configuration cannot change while S3 assets or pending uploads exist")

type Store struct {
	DB                   *state.Service
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
	for _, row := range s.DB.ListAssetStoreRowMetas() {
		if row.RemoteStatus == 1 || row.Staging() {
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
		secretID:    secret.VersionID,
		bucket:      s.Loader.MustLoadConfigStringValue(bucket),
		path:        s.Loader.MustLoadConfigStringValue(settings.LargeAssets.S3Path),
		region:      s.Loader.MustLoadConfigStringValue(region),
		endpoint:    s.Loader.MustLoadConfigStringValue(endpoint),
	}
}

// notifyContentMoved publishes every asset whose versions link the sha, after
// its content changed storage side or finished hashing.
func (s *Store) notifyContentMoved(sha string) {
	for _, assetID := range s.DB.ListAssetIDsBySha(sha) {
		if asset, ok := s.DB.GetAsset(assetID); ok {
			s.DB.NotifyAssetUpdate(asset)
		}
	}
}

// CreateAsset creates a new asset in directoryID (0 = the space root) of
// spaceID with its first version.
func (s *Store) CreateAsset(ctx context.Context, key string, spaceID, directoryID, author int32, blob []byte) (*apigen.Asset, error) {
	return s.CreateAssetFromReader(ctx, key, spaceID, directoryID, author, int64(len(blob)), bytes.NewReader(blob))
}

func (s *Store) CreateAssetFromReader(ctx context.Context, key string, spaceID, directoryID, author int32, sizeBytes int64, r io.Reader) (*apigen.Asset, error) {
	return s.writeVersion(ctx, sizeBytes, r,
		func(sha string) (*apigen.Asset, error) {
			return s.DB.CreateAssetWithVersion(key, spaceID, directoryID, author, sha, sizeBytes)
		})
}

// AppendAssetVersion appends the next version of an existing asset. The asset
// identity — key, space, directory — cannot change here.
func (s *Store) AppendAssetVersion(ctx context.Context, assetID, author int32, blob []byte) (*apigen.Asset, error) {
	return s.AppendAssetVersionFromReader(ctx, assetID, author, int64(len(blob)), bytes.NewReader(blob))
}

func (s *Store) AppendAssetVersionFromReader(ctx context.Context, assetID, author int32, sizeBytes int64, r io.Reader) (*apigen.Asset, error) {
	return s.writeVersion(ctx, sizeBytes, r,
		func(sha string) (*apigen.Asset, error) {
			return s.DB.AppendAssetVersion(assetID, author, sha, sizeBytes)
		})
}

// writeVersion stores the content first — deduplicated by sha256 against the
// content store — and creates the identity rows only once the content is
// durable, so an identity can never point at content that failed to land.
func (s *Store) writeVersion(ctx context.Context, sizeBytes int64, r io.Reader, insert func(sha string) (*apigen.Asset, error)) (*apigen.Asset, error) {
	if sizeBytes < 0 {
		return nil, fmt.Errorf("asset upload requires a content length")
	}
	if sizeBytes > math.MaxInt32 {
		return nil, fmt.Errorf("asset upload is too large: maximum supported size is %d bytes", math.MaxInt32)
	}
	if sizeBytes > InlineThresholdBytes {
		return s.writeLargeVersion(ctx, sizeBytes, r, insert)
	}
	blob, err := io.ReadAll(io.LimitReader(r, InlineThresholdBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read inline asset upload: %w", err)
	}
	if int64(len(blob)) != sizeBytes {
		return nil, fmt.Errorf("asset upload size changed while reading")
	}
	sha := hashBlob(blob)

	s.mu.Lock()
	defer s.mu.Unlock()
	created := false
	if _, ok := s.DB.GetAssetStoreRowBySha(sha); !ok {
		s.DB.InsertAssetStoreRow(newStoreID(), sha, sizeBytes, blob, 0, 0)
		created = true
	}
	version, err := insert(sha)
	if err != nil {
		if created {
			if reclaimErr := s.reclaimStoreContent(sha); reclaimErr != nil {
				slog.Warn("reclaim asset content after failed write", "sha256", sha, "err", reclaimErr)
			}
		}
		return nil, err
	}
	s.DB.NotifyAssetUpdate(version)
	return version, nil
}

func (s *Store) writeLargeVersion(ctx context.Context, sizeBytes int64, r io.Reader, insert func(sha string) (*apigen.Asset, error)) (*apigen.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.Config()
	storeID := newStoreID()
	s.DB.InsertAssetStoreRow(storeID, "", sizeBytes, nil, 0, 0)
	discardStaging := func() {
		if err := os.Remove(localPath(storeID)); err != nil && !os.IsNotExist(err) {
			slog.Warn("remove staged large asset", "store_id", storeID, "err", err)
		}
		s.DB.DeleteAssetStoreRow(storeID)
	}
	file, err := os.OpenFile(localPath(storeID), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		discardStaging()
		return nil, fmt.Errorf("stage large asset upload: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	if written, err := io.Copy(io.MultiWriter(file, hasher), r); err != nil {
		discardStaging()
		return nil, fmt.Errorf("stage large asset upload: %w", err)
	} else if written != sizeBytes {
		discardStaging()
		return nil, fmt.Errorf("asset upload size changed while reading")
	}
	if err := file.Sync(); err != nil {
		discardStaging()
		return nil, fmt.Errorf("sync staged large asset upload: %w", err)
	}
	sha := hex.EncodeToString(hasher.Sum(nil))

	if _, ok := s.DB.GetAssetStoreRowBySha(sha); ok {
		discardStaging()
	} else if s.Loader.MustLoadConfigBoolValue(cfg.Backup.Enabled) {
		client, bucket, err := s.s3Client(cfg)
		if err != nil {
			discardStaging()
			return nil, err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			discardStaging()
			return nil, fmt.Errorf("stage large asset upload: %w", err)
		}
		key := objectKey(s.Loader.MustLoadConfigStringValue(cfg.LargeAssets.S3Path), storeID)
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        aws.String(bucket),
			Key:           aws.String(key),
			Body:          file,
			ContentLength: aws.Int64(sizeBytes),
		}); err != nil {
			discardStaging()
			return nil, fmt.Errorf("write large asset to s3: %w", err)
		}
		s.DB.CompleteAssetStoreRow(storeID, sha, 0, 1)
		if err := os.Remove(localPath(storeID)); err != nil && !os.IsNotExist(err) {
			slog.Warn("remove staged large asset", "store_id", storeID, "err", err)
		}
	} else {
		if err := syncDir(ainit.StaticConfig.LargeAssetsDir); err != nil {
			discardStaging()
			return nil, fmt.Errorf("sync large asset directory: %w", err)
		}
		s.DB.CompleteAssetStoreRow(storeID, sha, 1, 0)
	}

	version, err := insert(sha)
	if err != nil {
		if reclaimErr := s.reclaimStoreContent(sha); reclaimErr != nil {
			slog.Warn("reclaim asset content after failed write", "sha256", sha, "err", reclaimErr)
		}
		return nil, err
	}
	s.DB.NotifyAssetUpdate(version)
	return version, nil
}

// reclaimStoreContent deletes the content-store row for sha, and its local
// file, once no version links it. The S3 object, if any, is retained for
// database restore points; the bucket lifecycle policy expires it.
func (s *Store) reclaimStoreContent(sha string) error {
	if sha == "" || s.DB.CountAssetVersionsBySha(sha) > 0 {
		return nil
	}
	row, ok := s.DB.GetAssetStoreRowBySha(sha)
	if !ok {
		return nil
	}
	if err := os.Remove(localPath(row.ID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete local large asset: %w", err)
	}
	s.DB.DeleteAssetStoreRow(row.ID)
	return nil
}

// OpenAsset resolves a pinned content version row id to its byte stream.
// sizeBytes is returned alongside so raw HTTP routes can set Content-Length.
func (s *Store) OpenAsset(ctx context.Context, assetVersionID int32) (sizeBytes int64, body io.ReadCloser, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.DB.GetAssetVersionJoined(assetVersionID)
	if !ok {
		return 0, nil, fmt.Errorf("asset version %d not found", assetVersionID)
	}
	switch {
	case r.Store.InlineSize > 0 || r.Version.SizeBytes == 0:
		return r.Version.SizeBytes, io.NopCloser(bytes.NewReader(r.Store.InlineBlob)), nil
	case r.Store.LocalStatus == 1:
		body, err := os.Open(localPath(r.Store.ID))
		if err != nil {
			return 0, nil, fmt.Errorf("read local large asset: %w", err)
		}
		return r.Version.SizeBytes, body, nil
	case r.Store.RemoteStatus == 1:
		body, err := s.openS3Asset(ctx, r.Store.ID)
		if err != nil {
			return 0, nil, err
		}
		return r.Version.SizeBytes, body, nil
	}
	return 0, nil, fmt.Errorf("asset version %d content is unavailable", assetVersionID)
}

func (s *Store) DeleteAsset(ctx context.Context, assetID int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	asset, hadVersions := s.DB.GetAsset(assetID)
	// Soft delete: version rows survive, so the content they reference stays
	// reclaim-exempt and the asset is recoverable at the DB level.
	s.DB.DeleteAsset(assetID)
	if hadVersions {
		s.DB.NotifyAssetDeleted(asset)
	}
	return nil
}

func (s *Store) RenameAsset(ctx context.Context, assetID int32, newKey string) (*apigen.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	asset, err := s.DB.RenameAssetKey(assetID, newKey)
	if err != nil {
		return nil, err
	}
	s.DB.NotifyAssetUpdate(asset)
	return asset, nil
}

func (s *Store) openS3Asset(ctx context.Context, storeID string) (io.ReadCloser, error) {
	cfg := s.Config()
	client, bucket, err := s.s3Client(cfg)
	if err != nil {
		return nil, err
	}
	key := objectKey(s.Loader.MustLoadConfigStringValue(cfg.LargeAssets.S3Path), storeID)
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
	if ref.VersionID == 0 {
		return "", nil
	}
	value, err := secrets.RevealByID(ref.VersionID)
	if err != nil {
		return "", err
	}
	return string(value), nil
}
