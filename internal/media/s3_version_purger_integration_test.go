//go:build integration

package media

import (
	"context"
	"errors"
	"fmt"
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

type interruptedVersionDelete struct {
	versionPurgeClient
	deleteCount int
	failAt      int
}

func (client *interruptedVersionDelete) DeleteObject(ctx context.Context, input *s3.DeleteObjectInput, options ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if input.VersionId != nil {
		client.deleteCount++
		if client.deleteCount == client.failAt {
			return nil, errors.New("synthetic interruption before version deletion")
		}
	}
	return client.versionPurgeClient.DeleteObject(ctx, input, options...)
}

func TestS3VersionPurgerExactKeyFencePaginationAndResume(t *testing.T) {
	endpoint := os.Getenv("MEDIA_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("MEDIA_S3_TEST_ENDPOINT is not set")
	}
	// This test creates and deletes versions. It is restricted to the disposable
	// verification MinIO profile, never the persistent local media bucket.
	if os.Getenv("MEDIA_S3_TEST_BUCKET") != "verification-media" ||
		os.Getenv("MEDIA_S3_TEST_ACCESS_KEY") != "synthetic-verification" {
		t.Skip("version purge integration requires the isolated verification object store")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	configuration, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("ap-south-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			os.Getenv("MEDIA_S3_TEST_ACCESS_KEY"), os.Getenv("MEDIA_S3_TEST_SECRET_KEY"), "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(configuration, func(options *s3.Options) {
		options.BaseEndpoint, options.UsePathStyle = aws.String(endpoint), true
	})
	bucket := "verification-media-purge-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucket), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	}); err != nil {
		t.Fatal(err)
	}
	key := objectKey(uuid.NewString(), "owner-purge-test", uuid.NewString())
	neighbor := key + "-neighbor"
	for index := 0; index < 5; index++ {
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(fmt.Sprintf("private-version-%d", index)),
		}); err != nil {
			t.Fatal(err)
		}
		if index == 1 || index == 3 {
			if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(neighbor), Body: strings.NewReader("different private key"),
	}); err != nil {
		t.Fatal(err)
	}
	// The target currently has a personal version. The purger must refuse to
	// replace it, leaving every version intact until an authorized tombstone
	// retry supplies a current delete marker.
	purger, err := newVersionPurgerWithPageSize(client, bucket, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := purger.purgeExactKey(ctx, key); !errors.Is(err, ErrDependency) {
		t.Fatalf("personal current version was accepted as purge fence: %v", err)
	}
	if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		t.Fatal(err)
	}
	// Fail after one exact-version removal. The nonpersonal current fence must
	// remain so a conditional upload cannot replay while the worker restarts.
	interrupted := &interruptedVersionDelete{versionPurgeClient: client, failAt: 2}
	purger, err = newVersionPurgerWithPageSize(interrupted, bucket, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := purger.purgeExactKey(ctx, key); err == nil {
		t.Fatal("injected mid-purge version deletion was reported as complete")
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader("replayed private bytes"), IfNoneMatch: aws.String("*"),
	}); err == nil {
		t.Fatal("conditional upload bypassed the nonpersonal purge fence")
	}
	// A new process sees the existing fence, verifies its exact bytes and
	// version ID, and resumes without adding a second marker or losing a page.
	purger, err = newVersionPurgerWithPageSize(client, bucket, 2)
	if err != nil {
		t.Fatal(err)
	}
	result, err := purger.purgeExactKey(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if result.versions+result.deleteMarkers != 5+3-1 {
		t.Fatalf("unexpected bounded exact-key deletion result: %+v", result)
	}
	versions, err := purger.listExact(ctx, key)
	if err != nil || len(versions) != 0 {
		t.Fatalf("target key retained versions or markers: versions=%v err=%v", versions, err)
	}
	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(neighbor)}); err != nil {
		t.Fatalf("prefix-neighbor key was deleted: %v", err)
	}
	// The primitive must refuse a non-versioned bucket before any destructive
	// operation; production IAM and owner policy remain separate gates.
	unversioned := "verification-media-plain-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(unversioned)}); err != nil {
		t.Fatal(err)
	}
	plainPurger, err := newVersionPurger(client, unversioned)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plainPurger.purgeExactKey(ctx, key); !errors.Is(err, ErrDependency) {
		t.Fatalf("unversioned bucket was accepted: %v", err)
	}
}
