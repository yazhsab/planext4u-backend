package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// This cleaner is deliberately not wired to the ordinary media runtime. A
// future owner worker must hold the purge-state then asset row locks, establish
// due-account/retention/hold authority, prove every signed upload grant expired
// with skew, and use separate version-delete IAM
// before calling it. A successful return proves only that this exact S3 key had
// no listed versions at the final read; it is not an account-erasure receipt.
type versionPurgeClient interface {
	GetBucketVersioning(context.Context, *s3.GetBucketVersioningInput, ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type versionPurger struct {
	client   versionPurgeClient
	bucket   string
	pageSize int32
}

type exactVersion struct {
	id     string
	marker bool
}

type versionPurgeSummary struct {
	versions      int
	deleteMarkers int
}

const (
	versionPurgePageSize   = int32(1000)
	versionPurgeMaxPages   = 1000
	versionPurgeMaxEntries = 100000
	fenceContentType       = "application/vnd.planext4u.purge-fence"
	fenceMetadataKey       = "planext4u-purge-fence"
	fenceMetadataValue     = "v1"
)

var fenceContents = []byte("planext4u nonpersonal purge fence v1")

func newVersionPurger(client versionPurgeClient, bucket string) (*versionPurger, error) {
	return newVersionPurgerWithPageSize(client, bucket, versionPurgePageSize)
}

func newVersionPurgerWithPageSize(client versionPurgeClient, bucket string, pageSize int32) (*versionPurger, error) {
	if client == nil || !validBucket(bucket) || pageSize < 1 || pageSize > 1000 {
		return nil, ErrInvalidRequest
	}
	return &versionPurger{client: client, bucket: bucket, pageSize: pageSize}, nil
}

// purgeExactKey never sends an unversioned DeleteObject. A conditional,
// nonpersonal current version fences any replayed conditional upload while
// historical versions and delete markers are removed by their exact IDs.
// Existing personal current content, unknown/null version IDs, incomplete
// pagination and provider errors leave cleanup pending.
func (purger *versionPurger) purgeExactKey(ctx context.Context, key string) (versionPurgeSummary, error) {
	var summary versionPurgeSummary
	if purger == nil || !validObjectKey(key) {
		return summary, ErrInvalidRequest
	}
	versioning, err := purger.client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(purger.bucket)})
	if err != nil || versioning == nil || versioning.Status != types.BucketVersioningStatusEnabled {
		return summary, fmt.Errorf("media version purge requires enabled bucket versioning: %w", errors.Join(ErrDependency, err))
	}
	fenceID, err := purger.putOrRecoverFence(ctx, key)
	if err != nil {
		return summary, err
	}
	entries, err := purger.listExact(ctx, key)
	if err != nil {
		return summary, err
	}
	fenceFound := false
	for _, entry := range entries {
		if entry.id == fenceID {
			if entry.marker {
				return summary, fmt.Errorf("media purge fence is a delete marker: %w", ErrDependency)
			}
			fenceFound = true
		}
	}
	if !fenceFound {
		return summary, fmt.Errorf("media purge fence is absent from version listing: %w", ErrDependency)
	}
	for _, entry := range entries {
		if entry.id == fenceID {
			continue
		}
		if _, err := purger.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(purger.bucket), Key: aws.String(key), VersionId: aws.String(entry.id),
		}); err != nil {
			return summary, fmt.Errorf("delete media object version: %w", err)
		}
		if entry.marker {
			summary.deleteMarkers++
		} else {
			summary.versions++
		}
	}
	remaining, err := purger.listExact(ctx, key)
	if err != nil {
		return summary, err
	}
	if len(remaining) != 1 || remaining[0].id != fenceID || remaining[0].marker {
		return summary, fmt.Errorf("media object versions remain behind purge fence: %w", ErrDependency)
	}
	if _, err := purger.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(purger.bucket), Key: aws.String(key), VersionId: aws.String(fenceID),
	}); err != nil {
		return summary, fmt.Errorf("delete media purge fence version: %w", err)
	}
	remaining, err = purger.listExact(ctx, key)
	if err != nil {
		return summary, err
	}
	if len(remaining) != 0 {
		return summary, fmt.Errorf("media object versions remain after purge: %w", ErrDependency)
	}
	return summary, nil
}

func (purger *versionPurger) putOrRecoverFence(ctx context.Context, key string) (string, error) {
	sum := sha256.Sum256(fenceContents)
	output, err := purger.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(purger.bucket),
		Key:            aws.String(key),
		Body:           bytes.NewReader(fenceContents),
		ContentLength:  aws.Int64(int64(len(fenceContents))),
		ContentType:    aws.String(fenceContentType),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
		IfNoneMatch:    aws.String("*"),
		Metadata:       map[string]string{fenceMetadataKey: fenceMetadataValue},
	})
	if err == nil {
		if output == nil || !validVersionID(aws.ToString(output.VersionId)) {
			return "", fmt.Errorf("media purge fence has no version ID: %w", ErrDependency)
		}
		return aws.ToString(output.VersionId), nil
	}
	// A timed-out Put may already have committed. Resume only if the current
	// exact version is the fixed nonpersonal fence. A personal current version
	// needs a separately authorized ordinary tombstone retry; never overwrite it.
	current, getErr := purger.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(purger.bucket), Key: aws.String(key)})
	if getErr != nil || current == nil || current.Body == nil {
		return "", fmt.Errorf("establish media purge fence: %w", err)
	}
	defer current.Body.Close()
	if !validVersionID(aws.ToString(current.VersionId)) ||
		aws.ToString(current.ContentType) != fenceContentType ||
		current.Metadata[fenceMetadataKey] != fenceMetadataValue ||
		aws.ToInt64(current.ContentLength) != int64(len(fenceContents)) {
		return "", fmt.Errorf("existing media object is not a purge fence: %w", ErrDependency)
	}
	contents, readErr := io.ReadAll(io.LimitReader(current.Body, int64(len(fenceContents)+1)))
	if readErr != nil || !bytes.Equal(contents, fenceContents) {
		return "", fmt.Errorf("existing media purge fence is unverified: %w", ErrDependency)
	}
	return aws.ToString(current.VersionId), nil
}

func (purger *versionPurger) listExact(ctx context.Context, key string) ([]exactVersion, error) {
	entries := make([]exactVersion, 0)
	seenVersions := make(map[string]struct{})
	seenMarkers := make(map[string]struct{})
	seenPages := make(map[string]struct{})
	var keyMarker, versionMarker *string
	for page := 0; page < versionPurgeMaxPages; page++ {
		response, err := purger.client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket: aws.String(purger.bucket), Prefix: aws.String(key), MaxKeys: aws.Int32(purger.pageSize),
			KeyMarker: keyMarker, VersionIdMarker: versionMarker,
		})
		if err != nil || response == nil {
			return nil, fmt.Errorf("list media object versions: %w", errors.Join(ErrDependency, err))
		}
		for _, item := range response.Versions {
			if aws.ToString(item.Key) != key {
				continue
			}
			id := aws.ToString(item.VersionId)
			if !validVersionID(id) {
				return nil, fmt.Errorf("media object has unversioned or missing version ID: %w", ErrDependency)
			}
			if _, exists := seenVersions[id]; exists {
				return nil, fmt.Errorf("media object version repeated across pages: %w", ErrDependency)
			}
			if _, exists := seenMarkers[id]; exists {
				return nil, fmt.Errorf("media version ID is also a delete marker: %w", ErrDependency)
			}
			seenVersions[id] = struct{}{}
			entries = append(entries, exactVersion{id: id})
		}
		for _, item := range response.DeleteMarkers {
			if aws.ToString(item.Key) != key {
				continue
			}
			id := aws.ToString(item.VersionId)
			if !validVersionID(id) {
				return nil, fmt.Errorf("media delete marker has missing version ID: %w", ErrDependency)
			}
			if _, exists := seenMarkers[id]; exists {
				return nil, fmt.Errorf("media delete marker repeated across pages: %w", ErrDependency)
			}
			if _, exists := seenVersions[id]; exists {
				return nil, fmt.Errorf("media delete marker ID is also an object version: %w", ErrDependency)
			}
			seenMarkers[id] = struct{}{}
			entries = append(entries, exactVersion{id: id, marker: true})
		}
		if len(entries) > versionPurgeMaxEntries {
			return nil, fmt.Errorf("media object version count exceeds bounded cleanup: %w", ErrDependency)
		}
		if response.IsTruncated == nil {
			return nil, fmt.Errorf("media version listing omitted completion flag: %w", ErrDependency)
		}
		if !aws.ToBool(response.IsTruncated) {
			return entries, nil
		}
		nextKey, nextVersion := aws.ToString(response.NextKeyMarker), aws.ToString(response.NextVersionIdMarker)
		if nextKey == "" || !strings.HasPrefix(nextKey, key) || (nextKey == key && nextVersion == "") {
			return nil, fmt.Errorf("media version listing omitted a valid continuation pair: %w", ErrDependency)
		}
		cursor := nextKey + "\x00" + nextVersion
		if _, exists := seenPages[cursor]; exists {
			return nil, fmt.Errorf("media version listing repeated a continuation cursor: %w", ErrDependency)
		}
		seenPages[cursor] = struct{}{}
		keyMarker, versionMarker = aws.String(nextKey), aws.String(nextVersion)
	}
	return nil, fmt.Errorf("media version listing exceeded bounded pages: %w", ErrDependency)
}

func validVersionID(value string) bool {
	return value != "" && value != "null" && len(value) <= 1024 && !strings.ContainsAny(value, "\x00\r\n")
}

var _ versionPurgeClient = (*s3.Client)(nil)
