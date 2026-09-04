package media

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

type UploadSigner interface {
	PresignPut(context.Context, string, ObjectMetadata, time.Time) (string, map[string]string, error)
}

type PresentationSigner interface {
	Present(context.Context, string, string, time.Time) (string, error)
}

type ObjectStore interface {
	Inspect(context.Context, string) (ObjectMetadata, error)
	Delete(context.Context, string) error
}

type MalwareScanner interface {
	Scan(context.Context, string) (ScanResult, error)
}

type Service struct {
	repository Repository
	signer     UploadSigner
	objects    ObjectStore
	scanner    MalwareScanner
	clock      func() time.Time
	newID      func() (string, error)
	uploadTTL  time.Duration
	presenter  PresentationSigner
}

func NewService(repository Repository, signer UploadSigner, objects ObjectStore, scanner MalwareScanner, uploadTTL time.Duration, clock func() time.Time) (*Service, error) {
	presenter, canPresent := signer.(PresentationSigner)
	if repository == nil || signer == nil || objects == nil || scanner == nil || clock == nil || !canPresent || uploadTTL < time.Minute || uploadTTL > 30*time.Minute {
		return nil, errors.New("invalid media service")
	}
	return &Service{repository: repository, signer: signer, objects: objects, scanner: scanner, uploadTTL: uploadTTL, clock: clock, newID: secureID, presenter: presenter}, nil
}

func (service *Service) Presign(ctx context.Context, tenantID, country, ownerID string, request PresignRequest) (UploadGrant, error) {
	if !safeID(tenantID) || !validCountry(country) || !safeID(ownerID) || !validPurpose(request.Purpose) || !validPresentationMetadata(request) || !validMetadata(ObjectMetadata{
		ContentType: request.ContentType, SizeBytes: request.SizeBytes, SHA256: request.SHA256,
	}, request.Purpose) {
		return UploadGrant{}, ErrInvalidRequest
	}
	id, err := service.newID()
	if err != nil {
		return UploadGrant{}, ErrDependency
	}
	now := service.clock().UTC()
	expiresAt := now.Add(service.uploadTTL)
	asset := Asset{ID: id, TenantID: tenantID, Country: country, OwnerID: ownerID, ObjectKey: objectKey(tenantID, ownerID, id), Purpose: request.Purpose,
		ContentType: request.ContentType, SizeBytes: request.SizeBytes, SHA256: strings.ToLower(request.SHA256), State: StatePendingUpload,
		AltText: strings.TrimSpace(request.AltText), Width: request.Width, Height: request.Height,
		CreatedAt: now, UploadExpiresAt: expiresAt, Version: 1}
	url, headers, err := service.signer.PresignPut(ctx, asset.ObjectKey, ObjectMetadata{ContentType: asset.ContentType, SizeBytes: asset.SizeBytes, SHA256: asset.SHA256}, expiresAt)
	if err != nil {
		return UploadGrant{}, ErrDependency
	}
	if err := service.repository.Create(ctx, asset); err != nil {
		return UploadGrant{}, err
	}
	return UploadGrant{Asset: asset, UploadURL: url, Headers: cloneHeaders(headers), ExpiresAt: expiresAt}, nil
}

func (service *Service) ResolvePresentations(ctx context.Context, tenantID, country string, assetIDs []string) (PresentationResponse, error) {
	if !safeID(tenantID) || !validCountry(country) || len(assetIDs) < 1 || len(assetIDs) > 50 {
		return PresentationResponse{}, ErrInvalidRequest
	}
	seen := map[string]bool{}
	for _, assetID := range assetIDs {
		if !safeID(assetID) || seen[assetID] {
			return PresentationResponse{}, ErrInvalidRequest
		}
		seen[assetID] = true
	}
	response := PresentationResponse{Items: []Presentation{}, UnavailableAssetIDs: []string{}}
	expiresAt := service.clock().UTC().Add(5 * time.Minute)
	for _, assetID := range assetIDs {
		asset, err := service.repository.GetPublic(ctx, tenantID, country, assetID)
		if errors.Is(err, ErrNotFound) {
			response.UnavailableAssetIDs = append(response.UnavailableAssetIDs, assetID)
			continue
		}
		if err != nil {
			return PresentationResponse{}, ErrDependency
		}
		if !validPublicPresentationAsset(asset) {
			response.UnavailableAssetIDs = append(response.UnavailableAssetIDs, assetID)
			continue
		}
		url, err := service.presenter.Present(ctx, asset.ObjectKey, asset.ContentType, expiresAt)
		if err != nil || !safePresentationURL(url) {
			return PresentationResponse{}, ErrDependency
		}
		expiry := expiresAt
		response.Items = append(response.Items, Presentation{
			AssetID: asset.ID, URL: url, ContentType: asset.ContentType, Width: asset.Width, Height: asset.Height,
			AltText: asset.AltText, Variants: []ResponsiveVariant{}, ExpiresAt: &expiry,
		})
	}
	return response, nil
}

func (service *Service) Complete(ctx context.Context, tenantID, ownerID, assetID string) (Asset, error) {
	asset, err := service.repository.Get(ctx, tenantID, ownerID, assetID)
	if err != nil {
		return Asset{}, err
	}
	if asset.State == StateReady || asset.State == StateRejected {
		return asset, nil
	}
	if asset.State == StateExpired || !service.clock().UTC().Before(asset.UploadExpiresAt) {
		if asset.State != StateExpired {
			asset.State, asset.Version = StateExpired, asset.Version+1
			_ = service.repository.Update(ctx, asset, asset.Version-1)
		}
		return Asset{}, ErrExpired
	}
	if asset.State != StatePendingUpload && asset.State != StatePendingScan {
		return Asset{}, ErrConflict
	}
	if asset.State == StatePendingUpload {
		metadata, inspectErr := service.objects.Inspect(ctx, asset.ObjectKey)
		if inspectErr != nil {
			return Asset{}, ErrDependency
		}
		if !metadataMatches(asset, metadata) {
			asset.State, asset.RejectedCode, asset.Version = StateRejected, "UPLOAD_METADATA_MISMATCH", asset.Version+1
			if updateErr := service.repository.Update(ctx, asset, asset.Version-1); updateErr != nil {
				return Asset{}, updateErr
			}
			_ = service.objects.Delete(ctx, asset.ObjectKey)
			return asset, nil
		}
		asset.State, asset.Version = StatePendingScan, asset.Version+1
		if err := service.repository.Update(ctx, asset, asset.Version-1); err != nil {
			return Asset{}, err
		}
	}
	result, err := service.scanner.Scan(ctx, asset.ObjectKey)
	if err != nil {
		return Asset{}, ErrDependency
	}
	if result.Clean {
		now := service.clock().UTC()
		asset.State, asset.ReadyAt, asset.RejectedCode = StateReady, &now, ""
	} else {
		asset.State, asset.RejectedCode = StateRejected, safeReason(result.ReasonCode)
		_ = service.objects.Delete(ctx, asset.ObjectKey)
	}
	asset.Version++
	if err := service.repository.Update(ctx, asset, asset.Version-1); err != nil {
		return Asset{}, err
	}
	return asset, nil
}

func (service *Service) Get(ctx context.Context, tenantID, ownerID, assetID string) (Asset, error) {
	if !safeID(assetID) {
		return Asset{}, ErrNotFound
	}
	return service.repository.Get(ctx, tenantID, ownerID, assetID)
}

func (service *Service) Delete(ctx context.Context, tenantID, ownerID, assetID string) error {
	asset, err := service.repository.Get(ctx, tenantID, ownerID, assetID)
	if err != nil {
		return err
	}
	if err := service.objects.Delete(ctx, asset.ObjectKey); err != nil {
		return ErrDependency
	}
	asset.State, asset.Version = StateDeleted, asset.Version+1
	return service.repository.Update(ctx, asset, asset.Version-1)
}

func metadataMatches(asset Asset, actual ObjectMetadata) bool {
	return asset.ContentType == actual.ContentType && asset.SizeBytes == actual.SizeBytes && strings.EqualFold(asset.SHA256, actual.SHA256)
}

func validMetadata(metadata ObjectMetadata, purpose Purpose) bool {
	limits := map[Purpose]int64{PurposeAvatar: 5 << 20, PurposeCatalogImage: 10 << 20, PurposeCompletionProof: 15 << 20, PurposeIdentityDocument: 15 << 20}
	allowed := map[Purpose]map[string]bool{
		PurposeAvatar:           {"image/jpeg": true, "image/png": true, "image/webp": true},
		PurposeCatalogImage:     {"image/jpeg": true, "image/png": true, "image/webp": true},
		PurposeCompletionProof:  {"image/jpeg": true, "image/png": true, "application/pdf": true},
		PurposeIdentityDocument: {"image/jpeg": true, "image/png": true, "application/pdf": true},
	}
	return metadata.SizeBytes > 0 && metadata.SizeBytes <= limits[purpose] && allowed[purpose][metadata.ContentType] && regexp.MustCompile(`^[a-fA-F0-9]{64}$`).MatchString(metadata.SHA256)
}

func validPurpose(purpose Purpose) bool {
	_, exists := map[Purpose]bool{PurposeAvatar: true, PurposeCatalogImage: true, PurposeCompletionProof: true, PurposeIdentityDocument: true}[purpose]
	return exists
}

func validPresentationMetadata(request PresignRequest) bool {
	altText := strings.TrimSpace(request.AltText)
	if request.Purpose == PurposeCatalogImage {
		return len(altText) >= 1 && len(altText) <= 240 && request.Width >= 1 && request.Width <= 16384 && request.Height >= 1 && request.Height <= 16384
	}
	return altText == "" && request.Width == 0 && request.Height == 0
}

func validPublicPresentationAsset(asset Asset) bool {
	return asset.Purpose == PurposeCatalogImage && asset.State == StateReady && strings.HasPrefix(asset.ContentType, "image/") &&
		len(strings.TrimSpace(asset.AltText)) >= 1 && len(asset.AltText) <= 240 && asset.Width >= 1 && asset.Width <= 16384 && asset.Height >= 1 && asset.Height <= 16384
}

func safePresentationURL(value string) bool {
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.ContainsAny(value, "\r\n") {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && !strings.ContainsAny(value, "\r\n")
}

func safeReason(value string) string {
	if regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,63}$`).MatchString(value) {
		return value
	}
	return "MALWARE_SCAN_REJECTED"
}

func safeID(value string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`).MatchString(value)
}

func validCountry(value string) bool {
	return regexp.MustCompile(`^[A-Z]{2}$`).MatchString(value)
}

func objectKey(tenantID, ownerID, id string) string {
	return fmt.Sprintf("tenants/%s/owners/%s/media/%s", tenantID, ownerID, id)
}

func secureID() (string, error) {
	value, err := uuid.NewRandom()
	return value.String(), err
}

func cloneHeaders(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
