//go:build integration

package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
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
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	body := encoded.Bytes()
	digest := sha256.Sum256(body)
	metadata := ObjectMetadata{ContentType: "image/png", SizeBytes: int64(len(body)), SHA256: hex.EncodeToString(digest[:])}
	key := "tenants/d1f47ba2-1ad1-46bf-aa23-2969a9ea656f/owners/owner/media/asset"
	uploadURL, headers, err := adapter.PresignPut(ctx, key, metadata, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if grant := inspectConditionalUploadGrant(uploadURL, headers); grant.Scheme != GrantProvenanceAWSConditionalSigV4 || grant.ValidUntil == nil {
		t.Fatalf("actual S3 upload grant is not classified as a signed conditional PUT: %+v", grant)
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
	request, _ = http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("scanned upload overwrite was not prevented: %d", response.StatusCode)
	}
	unsigned, err := http.Get(endpoint + "/" + bucket + "/" + key)
	if err != nil {
		t.Fatal(err)
	}
	_ = unsigned.Body.Close()
	if unsigned.StatusCode != http.StatusForbidden {
		t.Fatalf("private object leaked: %d", unsigned.StatusCode)
	}
	privateURL, err := adapter.SignPrivate(ctx, key, "image/png", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	private, err := http.Get(privateURL)
	if err != nil {
		t.Fatal(err)
	}
	download, err := io.ReadAll(private.Body)
	_ = private.Body.Close()
	if err != nil || private.StatusCode != 200 || !bytes.Equal(download, body) {
		t.Fatalf("private download=%d %v", private.StatusCode, err)
	}
	// A JPEG assertion containing PNG bytes must not become ready, even when
	// its checksum and all object metadata match the upload request.
	service, err := NewService(NewMemoryRepository(), adapter, adapter, &fakeScanner{result: ScanResult{Clean: true}}, 10*time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.Presign(ctx, "tenant-s3-test", "IN", "owner-s3-test", PresignRequest{Purpose: PurposeIdentityDocument, ContentType: "image/jpeg", SizeBytes: metadata.SizeBytes, SHA256: metadata.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequestWithContext(ctx, http.MethodPut, grant.UploadURL, bytes.NewReader(body))
	for name, value := range grant.Headers {
		request.Header.Set(name, value)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("mismatch upload failed: %d", response.StatusCode)
	}
	rejected, err := service.CompleteScoped(ctx, "tenant-s3-test", "IN", "owner-s3-test", grant.Asset.ID)
	if err != nil || rejected.State != StateRejected || rejected.RejectedCode != "UPLOAD_METADATA_MISMATCH" {
		t.Fatalf("mismatched actual bytes accepted: %+v %v", rejected, err)
	}
	if _, err := service.PrivateAccess(ctx, "tenant-s3-test", "IN", "owner-s3-test", grant.Asset.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("quarantined private access accepted: %v", err)
	}
	if err := adapter.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
}
