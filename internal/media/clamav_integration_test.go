//go:build integration

package media

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

func TestClamAVPrivateS3UploadQuarantineAndUnavailableRecovery(t *testing.T) {
	socket := os.Getenv("CLAMAV_TEST_SOCKET")
	if socket == "" {
		t.Skip("CLAMAV_TEST_SOCKET is not set")
	}
	endpoint := os.Getenv("MEDIA_S3_TEST_ENDPOINT")
	access, secret := os.Getenv("MEDIA_S3_TEST_ACCESS_KEY"), os.Getenv("MEDIA_S3_TEST_SECRET_KEY")
	if endpoint == "" || access == "" || secret == "" {
		t.Fatal("private S3 fixture is incomplete")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	configuration, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("ap-south-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(access, secret, "")))
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(configuration, func(options *s3.Options) { options.BaseEndpoint = aws.String(endpoint); options.UsePathStyle = true })
	bucket := "p4u-av-" + uuid.NewString()
	if _, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	var keys []string
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		for _, key := range keys {
			_, _ = client.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		}
		_, _ = client.DeleteBucket(cleanup, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})
	adapter, _ := NewS3Adapter(client, bucket, time.Now)
	scanner, err := NewClamAVScanner(adapter, socket, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = scanner.Ready(ctx); err != nil {
		t.Fatalf("real scanner is not ready: %v", err)
	}
	connection, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	_, _ = io.WriteString(connection, "zVERSION\x00")
	version, err := bufio.NewReader(connection).ReadString(0)
	_ = connection.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real scanner version/database: %s", version[:len(version)-1])
	repository := NewMemoryRepository()
	service, _ := NewService(repository, adapter, adapter, scanner, 10*time.Minute, time.Now)
	tenant, owner := uuid.NewString(), uuid.NewString()
	var picture bytes.Buffer
	if err = png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	upload := func(body []byte, contentType string) Asset {
		t.Helper()
		digest := sha256.Sum256(body)
		grant, err := service.Presign(ctx, tenant, "IN", owner, PresignRequest{Purpose: PurposeIdentityDocument, ContentType: contentType, SizeBytes: int64(len(body)), SHA256: hex.EncodeToString(digest[:])})
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, grant.Asset.ObjectKey)
		request, _ := http.NewRequestWithContext(ctx, http.MethodPut, grant.UploadURL, bytes.NewReader(body))
		for name, value := range grant.Headers {
			request.Header.Set(name, value)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("private upload failed: %d", response.StatusCode)
		}
		return grant.Asset
	}
	clean := upload(picture.Bytes(), "image/png")
	ready, err := service.CompleteScoped(ctx, tenant, "IN", owner, clean.ID)
	if err != nil || ready.State != StateReady {
		t.Fatalf("clean object not published: %+v %v", ready, err)
	}
	if _, err = service.PrivateAccess(ctx, tenant, "IN", owner, clean.ID); err != nil {
		t.Fatal(err)
	}
	// The official harmless AV test sample is decoded only inside the Linux test
	// process. A real PDF attachment is used: prefixing arbitrary bytes with a
	// PDF magic header does not create a valid test sample for an AV parser.
	sample, err := hex.DecodeString("58354f2150254041505b345c505a58353428505e2937434329377d2445494341522d5354414e444152442d414e544956495255532d544553542d46494c452124482b482a")
	if err != nil {
		t.Fatal(err)
	}
	rawKey := objectKey(tenant, owner, uuid.NewString())
	keys = append(keys, rawKey)
	if _, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(rawKey), Body: bytes.NewReader(sample), ContentType: aws.String("application/octet-stream")}); err != nil {
		t.Fatal(err)
	}
	if verdict, err := scanner.Scan(ctx, rawKey); err != nil || verdict.Clean || verdict.ReasonCode != "MALWARE_DETECTED" {
		t.Fatalf("standard engine test sample missed: %+v %v", verdict, err)
	}
	malicious := upload(clamAttachmentPDF(sample), "application/pdf")
	rejected, err := service.CompleteScoped(ctx, tenant, "IN", owner, malicious.ID)
	if err != nil || rejected.State != StateRejected || rejected.RejectedCode != "MALWARE_DETECTED" {
		t.Fatalf("real detection did not quarantine: state=%s code=%s err=%v", rejected.State, rejected.RejectedCode, err)
	}
	if _, err = service.PrivateAccess(ctx, tenant, "IN", owner, malicious.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("quarantined object became accessible")
	}
	if _, err = adapter.Inspect(ctx, malicious.ObjectKey); err == nil {
		t.Fatal("quarantined bytes were retained in the live bucket")
	}
	unavailable, _ := NewClamAVScanner(adapter, "/run/clamav/not-present.sock", time.Second)
	retryService, _ := NewService(repository, adapter, adapter, unavailable, 10*time.Minute, time.Now)
	pending := upload(picture.Bytes(), "image/png")
	if _, err = retryService.CompleteScoped(ctx, tenant, "IN", owner, pending.ID); !errors.Is(err, ErrDependency) {
		t.Fatal("missing scanner published an object")
	}
	current, err := service.GetScoped(ctx, tenant, "IN", owner, pending.ID)
	if err != nil || current.State != StatePendingScan {
		t.Fatalf("unavailable scan lost retry state: %s %v", current.State, err)
	}
	if _, err = service.PrivateAccess(ctx, tenant, "IN", owner, pending.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("unscanned object accessible")
	}
	if current, err = service.CompleteScoped(ctx, tenant, "IN", owner, pending.ID); err != nil || current.State != StateReady {
		t.Fatal("restored scanner could not recover pending upload")
	}
	// Advance only the consumer's test clock; do not replace official database
	// files or alter the real engine. A valid CLEAN verdict cannot bypass age.
	aged, _ := NewClamAVScannerWithFreshness(adapter, socket, 10*time.Second, 48*time.Hour, func() time.Time { return time.Now().UTC().Add(72 * time.Hour) })
	if err = aged.Ready(ctx); !errors.Is(err, ErrDependency) {
		t.Fatal("aged loaded definitions stayed ready")
	}
	agedService, _ := NewService(repository, adapter, adapter, aged, 10*time.Minute, time.Now)
	stalePending := upload(picture.Bytes(), "image/png")
	if _, err = agedService.CompleteScoped(ctx, tenant, "IN", owner, stalePending.ID); !errors.Is(err, ErrDependency) {
		t.Fatal("aged signatures published private bytes")
	}
	if _, err = service.PrivateAccess(ctx, tenant, "IN", owner, stalePending.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("aged scan granted private access")
	}
	if current, err = service.CompleteScoped(ctx, tenant, "IN", owner, stalePending.ID); err != nil || current.State != StateReady {
		t.Fatal("fresh-policy retry failed")
	}
	t.Log("signature freshness: actual loaded engine rejected with aged consumer clock; private evidence remained pending and recovered")
}

func clamAttachmentPDF(attachment []byte) []byte {
	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R /Names << /EmbeddedFiles << /Names [(eicar.txt) 5 0 R] >> >> >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R >>"),
		[]byte("<< /Length 0 >>\nstream\n\nendstream"),
		[]byte("<< /Type /Filespec /F (eicar.txt) /EF << /F 6 0 R >> >>"),
		append(append([]byte(fmt.Sprintf("<< /Type /EmbeddedFile /Length %d >>\nstream\n", len(attachment))), attachment...), []byte("\nendstream")...),
	}
	var document bytes.Buffer
	_, _ = document.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = document.Len()
		_, _ = fmt.Fprintf(&document, "%d 0 obj\n", index+1)
		_, _ = document.Write(object)
		_, _ = document.WriteString("\nendobj\n")
	}
	xref := document.Len()
	_, _ = fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		_, _ = fmt.Fprintf(&document, "%010d 00000 n \n", offset)
	}
	_, _ = fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return document.Bytes()
}
