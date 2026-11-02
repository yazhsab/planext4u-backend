package media

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type scriptedVersionList struct {
	versionPurgeClient
	pages []*s3.ListObjectVersionsOutput
	calls int
}

func (client *scriptedVersionList) ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	if client.calls >= len(client.pages) {
		return nil, errors.New("no scripted page")
	}
	page := client.pages[client.calls]
	client.calls++
	return page, nil
}

func TestVersionPurgerListsOnlyExactKeyAcrossPages(t *testing.T) {
	key := "tenants/test/owners/owner/media/asset"
	client := &scriptedVersionList{pages: []*s3.ListObjectVersionsOutput{
		{
			Versions: []types.ObjectVersion{{Key: aws.String(key), VersionId: aws.String("v1")},
				{Key: aws.String(key + "-neighbor"), VersionId: aws.String("neighbor")}},
			IsTruncated: aws.Bool(true), NextKeyMarker: aws.String(key), NextVersionIdMarker: aws.String("v1"),
		},
		{
			DeleteMarkers: []types.DeleteMarkerEntry{{Key: aws.String(key), VersionId: aws.String("m1")}},
			IsTruncated:   aws.Bool(false),
		},
	}}
	purger, err := newVersionPurgerWithPageSize(client, "verification-media", 2)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := purger.listExact(context.Background(), key)
	if err != nil || len(entries) != 2 || entries[0] != (exactVersion{id: "v1"}) || entries[1] != (exactVersion{id: "m1", marker: true}) || client.calls != 2 {
		t.Fatalf("exact-key page handling: entries=%+v calls=%d err=%v", entries, client.calls, err)
	}
}

func TestVersionPurgerRejectsUnknownVersionsAndBrokenPagination(t *testing.T) {
	key := "tenants/test/owners/owner/media/asset"
	for name, pages := range map[string][]*s3.ListObjectVersionsOutput{
		"null-version":           {{Versions: []types.ObjectVersion{{Key: aws.String(key), VersionId: aws.String("null")}}}},
		"missing-completion":     {{}},
		"missing-marker":         {{IsTruncated: aws.Bool(true)}},
		"missing-version-marker": {{IsTruncated: aws.Bool(true), NextKeyMarker: aws.String(key)}},
		"repeated-cursor": {
			{IsTruncated: aws.Bool(true), NextKeyMarker: aws.String(key), NextVersionIdMarker: aws.String("v1")},
			{IsTruncated: aws.Bool(true), NextKeyMarker: aws.String(key), NextVersionIdMarker: aws.String("v1")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &scriptedVersionList{pages: pages}
			purger, err := newVersionPurgerWithPageSize(client, "verification-media", 2)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := purger.listExact(context.Background(), key); !errors.Is(err, ErrDependency) {
				t.Fatalf("unsafe version listing accepted: %v", err)
			}
		})
	}
}
