package chunks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func testTarget(endpoint string) *s3Target {
	client := s3.New(s3.Options{
		Region:           "us-east-1",
		BaseEndpoint:     aws.String(endpoint),
		UsePathStyle:     true,
		Credentials:      credentials.NewStaticCredentialsProvider("key", "secret", ""),
		RetryMaxAttempts: 1,
	})
	return newS3Target(client, "bucket", "/opendeploy/archive/")
}

func TestPresignedUploadSignsTheHashAndTheSizeAsHeaders(t *testing.T) {
	target := testTarget("http://bucket.test:9000")
	raw, headers, err := target.PresignPut(context.Background(), "metrics/20241004/d42_n3_2.parquet", sum(7), 4096)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/bucket/opendeploy/archive/metrics/20241004/d42_n3_2.parquet" {
		t.Fatalf("upload path %s", u.Path)
	}
	q := u.Query()
	if got := q.Get("X-Amz-SignedHeaders"); got != "content-length;host;x-amz-checksum-sha256" {
		t.Fatalf("signed headers %q: the bucket would not hold the upload to the hash and the size", got)
	}
	for name := range q {
		if strings.EqualFold(name, "x-amz-checksum-sha256") {
			t.Fatal("the hash was moved into the query string, where some buckets ignore it")
		}
	}
	lower := map[string]string{}
	for name, value := range headers {
		lower[strings.ToLower(name)] = value
	}
	if lower["x-amz-checksum-sha256"] != base64.StdEncoding.EncodeToString(sum(7)) || lower["content-length"] != "4096" {
		t.Fatalf("headers for the node to send: %v", headers)
	}
	if _, ok := lower["host"]; ok {
		t.Fatal("the node was told to send a Host header")
	}
	if q.Get("X-Amz-Expires") != "21600" {
		t.Fatalf("the URL expires after %s seconds", q.Get("X-Amz-Expires"))
	}
}

func TestHeadReadsSizeAndHashAndTellsMissingFromFailing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.Header.Get("x-amz-checksum-mode") != "ENABLED" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/opendeploy/archive/held"):
			w.Header().Set("Content-Length", "4096")
			w.Header().Set("x-amz-checksum-sha256", base64.StdEncoding.EncodeToString(sum(7)))
		case strings.HasSuffix(r.URL.Path, "/opendeploy/archive/unhashed"):
			w.Header().Set("Content-Length", "12")
		case strings.HasSuffix(r.URL.Path, "/opendeploy/archive/denied"):
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	target := testTarget(srv.URL)
	ctx := context.Background()

	obj, ok, err := target.Head(ctx, "held")
	if err != nil || !ok || obj.Size != 4096 || !bytes.Equal(obj.Sha256, sum(7)) {
		t.Fatalf("held object: %+v ok=%v err=%v", obj, ok, err)
	}
	obj, ok, err = target.Head(ctx, "unhashed")
	if err != nil || !ok || obj.Size != 12 || obj.Sha256 != nil {
		t.Fatalf("object without a stored hash: %+v ok=%v err=%v", obj, ok, err)
	}
	if _, ok, err := target.Head(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing object: ok=%v err=%v", ok, err)
	}
	if _, ok, err := target.Head(ctx, "denied"); err == nil || ok {
		t.Fatalf("a refused request read as ok=%v err=%v, want an error", ok, err)
	}
}

func realBucketTarget(t *testing.T) *s3Target {
	endpoint, bucket := os.Getenv("ARCHIVE_S3_TEST_ENDPOINT"), os.Getenv("ARCHIVE_S3_TEST_BUCKET")
	if bucket == "" {
		t.Skip("set ARCHIVE_S3_TEST_BUCKET, _ACCESS_KEY_ID and _SECRET_ACCESS_KEY (and _ENDPOINT, _REGION for a bucket outside AWS) to run against a real bucket")
	}
	region := os.Getenv("ARCHIVE_S3_TEST_REGION")
	if region == "" {
		region = "us-east-1"
	}
	client := s3.New(s3.Options{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(os.Getenv("ARCHIVE_S3_TEST_ACCESS_KEY_ID"), os.Getenv("ARCHIVE_S3_TEST_SECRET_ACCESS_KEY"), ""),
	}, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})
	return newS3Target(client, bucket, fmt.Sprintf("opendeploy/archive-test/%d", time.Now().UnixNano()))
}

func putPresigned(t *testing.T, target *s3Target, key string, sum []byte, size int64, body []byte) int {
	t.Helper()
	raw, headers, err := target.PresignPut(context.Background(), key, sum, size)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, raw, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(body))
	for name, value := range headers {
		if !strings.EqualFold(name, "Content-Length") {
			req.Header.Set(name, value)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestRealBucketHoldsAnUploadToItsHashAndSize(t *testing.T) {
	target := realBucketTarget(t)
	ctx := context.Background()
	body := bytes.Repeat([]byte("sealed chunk "), 4096)
	digest := sha256.Sum256(body)
	key := "metrics/20241004/d42_n3_2.parquet"
	t.Cleanup(func() {
		for _, k := range []string{key, "other-content", "other-size"} {
			_, _ = target.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(target.bucket), Key: aws.String(target.key(k))})
		}
	})

	if _, found, err := target.Head(ctx, key); err != nil || found {
		t.Fatalf("before the upload: found=%v err=%v", found, err)
	}
	if status := putPresigned(t, target, key, digest[:], int64(len(body)), body); status/100 != 2 {
		t.Fatalf("upload answered %d", status)
	}
	obj, found, err := target.Head(ctx, key)
	if err != nil || !found {
		t.Fatalf("after the upload: found=%v err=%v", found, err)
	}
	if obj.Size != int64(len(body)) {
		t.Fatalf("stored size %d, sent %d", obj.Size, len(body))
	}
	if obj.Sha256 == nil {
		t.Log("the bucket reports no SHA-256 on HEAD; the primary accepts the upload on its size")
	} else if !bytes.Equal(obj.Sha256, digest[:]) {
		t.Fatalf("stored hash %x, sent %x", obj.Sha256, digest)
	}

	other := bytes.Repeat([]byte("another body "), 4096)
	if status := putPresigned(t, target, "other-content", digest[:], int64(len(body)), other); status/100 == 2 {
		t.Fatal("the bucket took other content of the same size under a URL signed for the hash")
	}
	if _, found, _ := target.Head(ctx, "other-content"); found {
		t.Fatal("the refused content is in the bucket")
	}
	if status := putPresigned(t, target, "other-size", digest[:], int64(len(body)), body[:len(body)-13]); status/100 == 2 {
		t.Fatal("the bucket took a shorter body under a URL signed for the size")
	}
}
