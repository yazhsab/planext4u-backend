//go:build integration

package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

func TestS3AdapterPresignInspectAndDeleteAgainstObjectStore(t *testing.T) {
	endpoint := os.Getenv("MEDIA_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("MEDIA_S3_TEST_ENDPOINT is not set")
	}
	accessKey, secretKey := os.Getenv("MEDIA_S3_TEST_ACCESS_KEY"), os.Getenv("MEDIA_S3_TEST_SECRET_KEY")
	bucket := os.Getenv("MEDIA_S3_TEST_BUCKET")
	if accessKey == "" || secretKey == "" || bucket == "" {
		t.Fatal("object-store integration credentials are incomplete")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	configuration, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("ap-south-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(configuration, func(options *s3.Options) {
		options.BaseEndpoint, options.UsePathStyle = aws.String(endpoint), true
	})
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		var apiError smithy.APIError
		if !errors.As(err, &apiError) || (apiError.ErrorCode() != "BucketAlreadyOwnedByYou" && apiError.ErrorCode() != "BucketAlreadyExists") {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	adapter, err := NewS3Adapter(client, bucket, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("p4u-media"), 512)
	digest := sha256.Sum256(body)
	metadata := ObjectMetadata{ContentType: "image/jpeg", SizeBytes: int64(len(body)), SHA256: hex.EncodeToString(digest[:])}
	key := "tenants/d1f47ba2-1ad1-46bf-aa23-2969a9ea656f/owners/owner/media/asset"
	uploadURL, headers, err := adapter.PresignPut(ctx, key, metadata, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("upload status=%d", response.StatusCode)
	}
	actual, err := adapter.Inspect(ctx, key)
	if err != nil || actual != metadata {
		t.Fatalf("metadata=%#v err=%v", actual, err)
	}
	if err := adapter.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
}
