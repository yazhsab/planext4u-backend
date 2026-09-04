package media

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type s3API interface {
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

func (adapter *S3Adapter) Ready(ctx context.Context) error {
	if _, err := adapter.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(adapter.bucket)}); err != nil {
		return fmt.Errorf("check media bucket readiness: %w", err)
	}
	return nil
}

type s3Presigner interface {
	PresignPutObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignGetObject(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

type S3Adapter struct {
	client    s3API
	presigner s3Presigner
	bucket    string
	clock     func() time.Time
}

func (adapter *S3Adapter) Present(ctx context.Context, key, contentType string, expiresAt time.Time) (string, error) {
	if !validObjectKey(key) || !strings.HasPrefix(contentType, "image/") {
		return "", ErrInvalidRequest
	}
	expires := expiresAt.UTC().Sub(adapter.clock().UTC())
	if expires < time.Minute || expires > 15*time.Minute {
		return "", ErrInvalidRequest
	}
	request, err := adapter.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(adapter.bucket), Key: aws.String(key), ResponseContentType: aws.String(contentType),
	}, func(options *s3.PresignOptions) { options.Expires = expires })
	if err != nil {
		return "", fmt.Errorf("presign media presentation: %w", err)
	}
	return request.URL, nil
}

func NewS3Adapter(client *s3.Client, bucket string, clock func() time.Time) (*S3Adapter, error) {
	if client == nil || !validBucket(bucket) || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &S3Adapter{client: client, presigner: s3.NewPresignClient(client), bucket: bucket, clock: clock}, nil
}

func (adapter *S3Adapter) PresignPut(ctx context.Context, key string, metadata ObjectMetadata, expiresAt time.Time) (string, map[string]string, error) {
	if !validObjectKey(key) || !validMetadataForS3(metadata) {
		return "", nil, ErrInvalidRequest
	}
	expires := expiresAt.UTC().Sub(adapter.clock().UTC())
	if expires < time.Minute || expires > 30*time.Minute {
		return "", nil, ErrInvalidRequest
	}
	checksumBytes, err := hex.DecodeString(strings.ToLower(metadata.SHA256))
	if err != nil {
		return "", nil, ErrInvalidRequest
	}
	checksum := base64.StdEncoding.EncodeToString(checksumBytes)
	request, err := adapter.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(adapter.bucket),
		Key:            aws.String(key),
		ContentType:    aws.String(metadata.ContentType),
		ContentLength:  aws.Int64(metadata.SizeBytes),
		ChecksumSHA256: aws.String(checksum),
		Metadata:       map[string]string{"sha256": strings.ToLower(metadata.SHA256)},
	}, func(options *s3.PresignOptions) { options.Expires = expires })
	if err != nil {
		return "", nil, fmt.Errorf("presign media upload: %w", err)
	}
	return request.URL, map[string]string{
		"content-type":          metadata.ContentType,
		"x-amz-checksum-sha256": checksum,
		"x-amz-meta-sha256":     strings.ToLower(metadata.SHA256),
	}, nil
}

func (adapter *S3Adapter) Inspect(ctx context.Context, key string) (ObjectMetadata, error) {
	if !validObjectKey(key) {
		return ObjectMetadata{}, ErrInvalidRequest
	}
	result, err := adapter.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(adapter.bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		return ObjectMetadata{}, fmt.Errorf("inspect media object: %w", err)
	}
	checksum := strings.ToLower(strings.TrimSpace(result.Metadata["sha256"]))
	if checksum == "" && result.ChecksumSHA256 != nil {
		decoded, decodeErr := base64.StdEncoding.DecodeString(aws.ToString(result.ChecksumSHA256))
		if decodeErr == nil {
			checksum = hex.EncodeToString(decoded)
		}
	}
	metadata := ObjectMetadata{
		ContentType: strings.TrimSpace(aws.ToString(result.ContentType)),
		SizeBytes:   aws.ToInt64(result.ContentLength),
		SHA256:      checksum,
	}
	if !validMetadataForS3(metadata) {
		return ObjectMetadata{}, errors.New("media object metadata is incomplete")
	}
	return metadata, nil
}

func (adapter *S3Adapter) Delete(ctx context.Context, key string) error {
	if !validObjectKey(key) {
		return ErrInvalidRequest
	}
	if _, err := adapter.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(adapter.bucket), Key: aws.String(key),
	}); err != nil {
		return fmt.Errorf("delete media object: %w", err)
	}
	return nil
}

func validBucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' && character != '.' {
			return false
		}
	}
	return !strings.Contains(value, "..")
}

func validObjectKey(value string) bool {
	return strings.HasPrefix(value, "tenants/") && len(value) <= 1024 && !strings.Contains(value, "..") && !strings.ContainsAny(value, "\x00\r\n")
}

func validMetadataForS3(value ObjectMetadata) bool {
	return value.SizeBytes > 0 && value.SizeBytes <= 50<<20 && value.ContentType != "" &&
		len(value.ContentType) <= 128 && regexpSHA256.MatchString(value.SHA256)
}

var regexpSHA256 = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

var _ UploadSigner = (*S3Adapter)(nil)
var _ PresentationSigner = (*S3Adapter)(nil)
var _ ObjectStore = (*S3Adapter)(nil)
