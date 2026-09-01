package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type HTTPMalwareScanner struct {
	endpoint *url.URL
	client   *http.Client
	token    string
}

func NewHTTPMalwareScanner(endpoint *url.URL, client *http.Client, token string) (*HTTPMalwareScanner, error) {
	token = strings.TrimSpace(token)
	if endpoint == nil || !endpoint.IsAbs() || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		client == nil || client.Timeout <= 0 || len(token) < 16 || len(token) > 4096 || strings.ContainsAny(token, "\r\n") {
		return nil, ErrInvalidRequest
	}
	copyURL := *endpoint
	return &HTTPMalwareScanner{endpoint: &copyURL, client: client, token: token}, nil
}

func (scanner *HTTPMalwareScanner) Scan(ctx context.Context, objectKey string) (ScanResult, error) {
	if !validObjectKey(objectKey) {
		return ScanResult{}, ErrInvalidRequest
	}
	payload, err := json.Marshal(map[string]string{"object_key": objectKey})
	if err != nil {
		return ScanResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, scanner.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return ScanResult{}, err
	}
	request.Header.Set("Authorization", "Bearer "+scanner.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := scanner.client.Do(request)
	if err != nil {
		return ScanResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return ScanResult{}, errors.New("malware scanner rejected the request")
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16*1024))
	decoder.DisallowUnknownFields()
	var result ScanResult
	if err := decoder.Decode(&result); err != nil {
		return ScanResult{}, errors.New("malware scanner returned an invalid response")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || (!result.Clean && safeReason(result.ReasonCode) != result.ReasonCode) || (result.Clean && result.ReasonCode != "") {
		return ScanResult{}, errors.New("malware scanner returned an invalid result")
	}
	return result, nil
}

var _ MalwareScanner = (*HTTPMalwareScanner)(nil)
