package media

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestPrivateObjectClientAndPublicPresignerKeepDistinctHosts(t *testing.T) {
	configuration := aws.Config{Region: "ap-south-1", Credentials: credentials.NewStaticCredentialsProvider("synthetic-access", "synthetic-secret", "")}
	client := func(endpoint string) *s3.Client {
		return s3.NewFromConfig(configuration, func(options *s3.Options) { options.BaseEndpoint = aws.String(endpoint); options.UsePathStyle = true })
	}
	private, public := client("https://object-store.internal"), client("https://uploads.example")
	now := time.Now().UTC()
	adapter, err := NewS3AdapterWithPresignClient(private, public, "p4u-private-test", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	key := "tenants/tenant/owners/owner/media/asset"
	grant, _, err := adapter.PresignPut(context.Background(), key, ObjectMetadata{ContentType: "image/png", SizeBytes: 128, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	upload, _ := url.Parse(grant)
	if upload.Host != "uploads.example" || upload.Query().Get("X-Amz-Signature") == "" {
		t.Fatal("upload was not signed against the public host")
	}
	downloadURL, err := adapter.SignPrivate(context.Background(), key, "image/png", now.Add(2*time.Minute))
	download, _ := url.Parse(downloadURL)
	if err != nil || download.Host != "uploads.example" || download.Query().Get("X-Amz-Signature") == "" {
		t.Fatal("download was not signed against the public host")
	}
	if adapter.client != private {
		t.Fatal("public signer replaced the private inspection client")
	}
}
