// Local development provisioning only. Production buckets and policies are
// provisioned through deployment infrastructure, never by the media runtime.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

func main() {
	if err := initialize(); err != nil {
		fmt.Fprintln(os.Stderr, "Private local bucket initialization failed; existing data is retained.")
		os.Exit(1)
	}
	fmt.Println("Private local bucket exists; no public policy or existing object was changed.")
}

func initialize() error {
	endpoint, err := url.Parse(os.Getenv("S3_ENDPOINT"))
	if err != nil || os.Getenv("APP_ENV") != "development" || endpoint.Scheme != "http" || endpoint.Host != "object-store:9000" || endpoint.Path != "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || os.Getenv("MEDIA_BUCKET") != "planext4u-phase2-private" {
		return errors.New("local provisioning boundary denied")
	}
	access, err := os.ReadFile(os.Getenv("S3_ACCESS_KEY_ID_FILE"))
	if err != nil {
		return err
	}
	secret, err := os.ReadFile(os.Getenv("S3_SECRET_ACCESS_KEY_FILE"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	configuration, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("ap-south-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(strings.TrimSpace(string(access)), strings.TrimSpace(string(secret)), "")))
	if err != nil {
		return err
	}
	client := s3.NewFromConfig(configuration, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint.String())
		options.UsePathStyle = true
	})
	bucket := aws.String("planext4u-phase2-private")
	if _, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: bucket}); err == nil {
		return nil
	}
	var apiError smithy.APIError
	if !errors.As(err, &apiError) || (apiError.ErrorCode() != "NotFound" && apiError.ErrorCode() != "NoSuchBucket") {
		return err
	}
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: bucket})
	return err
}
