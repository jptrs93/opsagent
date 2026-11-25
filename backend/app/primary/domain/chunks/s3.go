package chunks

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/jptrs93/opsagent/backend/apigen"
)

var ErrArchiveS3Config = errors.New("archive bucket is not configured")

const presignTTL = 6 * time.Hour

type s3Target struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	prefix  string
}

func (s *Service) openS3(cfg *apigen.ClusterSettings) (Target, error) {
	secret := ""
	if ref := cfg.Backup.S3SecretAccessKey; ref.Present && ref.Value.Valid() {
		value, err := s.Secrets.RevealByRef(ref.Value.Ref())
		if err != nil {
			return nil, fmt.Errorf("reveal archive S3 secret access key: %w", err)
		}
		secret = string(value)
	}
	accessKeyID := s.Loader.MustLoadStringSetting(cfg.Backup.S3AccessKeyID)
	bucket := s.Loader.MustLoadStringSetting(cfg.Backup.S3Bucket)
	region := s.Loader.MustLoadStringSetting(cfg.Backup.S3Region)
	endpoint := s.Loader.MustLoadStringSetting(cfg.Backup.S3Endpoint)
	if accessKeyID == "" || secret == "" || bucket == "" || region == "" {
		return nil, fmt.Errorf("%w: access key ID, secret access key, bucket, and region are required", ErrArchiveS3Config)
	}
	client := s3.NewFromConfig(aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(accessKeyID, secret, ""),
	}, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})
	return newS3Target(client, bucket, s.Loader.MustLoadStringSetting(cfg.Archive.S3Path)), nil
}

func newS3Target(client *s3.Client, bucket, prefix string) *s3Target {
	// The default presigner moves x-amz-checksum-sha256 into the query
	// string, where some S3 implementations ignore it and accept any body.
	// Signing it as a header is what makes the bucket refuse other content.
	presign := s3.NewPresignClient(client, func(o *s3.PresignOptions) {
		o.Presigner = v4.NewSigner(func(so *v4.SignerOptions) {
			so.DisableHeaderHoisting = true
			so.DisableURIPathEscaping = true
		})
	})
	return &s3Target{client: client, presign: presign, bucket: bucket, prefix: strings.Trim(prefix, "/")}
}

func (t *s3Target) key(key string) string {
	return path.Join(t.prefix, key)
}

func (t *s3Target) Head(ctx context.Context, key string) (Object, bool, error) {
	out, err := t.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:       aws.String(t.bucket),
		Key:          aws.String(t.key(key)),
		ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		var notFound *types.NotFound
		var respErr *smithyhttp.ResponseError
		if errors.As(err, &notFound) || errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound {
			return Object{}, false, nil
		}
		return Object{}, false, err
	}
	obj := Object{Size: aws.ToInt64(out.ContentLength)}
	if sum, err := base64.StdEncoding.DecodeString(aws.ToString(out.ChecksumSHA256)); err == nil && len(sum) == 32 {
		obj.Sha256 = sum
	}
	return obj, true, nil
}

func (t *s3Target) PresignPut(ctx context.Context, key string, sha256 []byte, size int64) (string, map[string]string, error) {
	req, err := t.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(t.bucket),
		Key:            aws.String(t.key(key)),
		ContentLength:  aws.Int64(size),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sha256)),
	}, s3.WithPresignExpires(presignTTL))
	if err != nil {
		return "", nil, err
	}
	headers := map[string]string{}
	for name, values := range req.SignedHeader {
		if strings.EqualFold(name, "host") || len(values) == 0 {
			continue
		}
		headers[name] = values[0]
	}
	return req.URL, headers, nil
}
