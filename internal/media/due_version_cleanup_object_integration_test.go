//go:build integration

package media

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
)

// This test joins the due-request database authority and the real versioned
// object-store deletion in one disposable verification run. Neither the local
// application database nor its persistent bucket is eligible for this fixture.
func TestDueVersionCleanerDisposableDatabaseAndObjectStore(t *testing.T) {
	if os.Getenv("MEDIA_S3_TEST_ENDPOINT") == "" ||
		os.Getenv("MEDIA_S3_TEST_BUCKET") != "verification-media" ||
		os.Getenv("MEDIA_S3_TEST_ACCESS_KEY") != "synthetic-verification" ||
		os.Getenv("MEDIA_PURGE_TEST_DISPOSABLE") != "1" {
		t.Skip("isolated disposable PostgreSQL and versioned object store are required")
	}
	ctx, owner, cleanup, _, tenant, subject, request := dueVersionFixture(t)
	if _, err := owner.Exec(ctx, `UPDATE identity.deletion_country_policies SET retention_interval=interval '0'
	 WHERE tenant_id=$1 AND country='IN' AND owner='media'`, tenant); err != nil {
		t.Fatal(err)
	}
	asset := insertDueMediaAsset(t, ctx, owner, tenant, subject, "IN", "IDENTITY_DOCUMENT", "AWS_SIGV4_IF_NONE_MATCH", time.Now().Add(-15*time.Minute))
	configuration, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("ap-south-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			os.Getenv("MEDIA_S3_TEST_ACCESS_KEY"), os.Getenv("MEDIA_S3_TEST_SECRET_KEY"), "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(configuration, func(options *s3.Options) {
		options.BaseEndpoint, options.UsePathStyle = aws.String(os.Getenv("MEDIA_S3_TEST_ENDPOINT")), true
	})
	bucket := "verification-media-due-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucket), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	}); err != nil {
		t.Fatal(err)
	}
	key := objectKey(tenant, subject, asset)
	neighbor := key + "-retained"
	for _, body := range []string{"private-version-one", "private-version-two"} {
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(body),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(neighbor), Body: strings.NewReader("different asset"),
	}); err != nil {
		t.Fatal(err)
	}
	cleaner, err := NewDueVersionCleaner(cleanup, client, bucket)
	if err != nil {
		t.Fatal(err)
	}
	result, err := cleaner.PurgeDueAsset(ctx, request, asset)
	if err != nil || result.AlreadyPurged || result.VersionsDeleted < 2 || result.MarkersDeleted < 1 {
		t.Fatalf("combined due cleanup result=%+v err=%v", result, err)
	}
	purger, err := newVersionPurger(client, bucket)
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := purger.listExact(ctx, key)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("personal versions remain after database mark: count=%d err=%v", len(remaining), err)
	}
	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(neighbor)}); err != nil {
		t.Fatalf("neighbor key was removed: %v", err)
	}
	var evidenceCount int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM media.object_purge_evidence e
	 JOIN media.object_purge_state s ON s.asset_id=e.asset_id
	 WHERE e.asset_id=$1 AND e.request_id=$2 AND s.purged_at=e.recorded_at
	 AND e.deleted_versions=$3 AND e.deleted_markers=$4`, asset, request,
		result.VersionsDeleted, result.MarkersDeleted).Scan(&evidenceCount); err != nil || evidenceCount != 1 {
		t.Fatalf("matching durable object evidence missing: count=%d err=%v", evidenceCount, err)
	}
	retry, err := cleaner.PurgeDueAsset(ctx, request, asset)
	if err != nil || !retry.AlreadyPurged {
		t.Fatalf("completed cleanup was not idempotent: result=%+v err=%v", retry, err)
	}
}
