package payment

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxProviderBodyBytes = 128 * 1024

type HTTPProviderConfig struct {
	BaseURL   *url.URL
	Client    *http.Client
	PublicKey string
	Secret    []byte
	Timeout   time.Duration
}

type RazorpayProvider struct {
	base      *url.URL
	endpoint  *url.URL
	client    *http.Client
	publicKey string
	secret    []byte
	timeout   time.Duration
}

type PaystackProvider struct {
	base      *url.URL
	endpoint  *url.URL
	client    *http.Client
	publicKey string
	secret    []byte
	timeout   time.Duration
}

func NewRazorpayProvider(config HTTPProviderConfig) (*RazorpayProvider, error) {
	endpoint, client, err := providerHTTPConfiguration(config, "https://api.razorpay.com", "/v1/orders", "rzp_")
	if err != nil {
		return nil, err
	}
	base := *endpoint
	base.Path = ""
	return &RazorpayProvider{base: &base, endpoint: endpoint, client: client, publicKey: config.PublicKey, secret: append([]byte(nil), config.Secret...), timeout: config.Timeout}, nil
}

func NewPaystackProvider(config HTTPProviderConfig) (*PaystackProvider, error) {
	endpoint, client, err := providerHTTPConfiguration(config, "https://api.paystack.co", "/transaction/initialize", "pk_")
	if err != nil {
		return nil, err
	}
	base := *endpoint
	base.Path = ""
	return &PaystackProvider{base: &base, endpoint: endpoint, client: client, publicKey: config.PublicKey, secret: append([]byte(nil), config.Secret...), timeout: config.Timeout}, nil
}

func providerHTTPConfiguration(config HTTPProviderConfig, defaultBase, path, publicPrefix string) (*url.URL, *http.Client, error) {
	if !validPublicKey(config.PublicKey, publicPrefix) || len(config.Secret) < 16 || len(config.Secret) > 4096 || config.Timeout <= 0 || config.Timeout > 30*time.Second {
		return nil, nil, ErrInvalidRequest
	}
	base := config.BaseURL
	if base == nil {
		parsed, _ := url.Parse(defaultBase)
		base = parsed
	}
	if !base.IsAbs() || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" ||
		(base.Scheme != "https" && !(base.Scheme == "http" && isLoopbackHost(base.Hostname()))) {
		return nil, nil, ErrInvalidRequest
	}
	copy := *base
	endpoint := copy.ResolveReference(&url.URL{Path: path})
	if endpoint.Scheme != copy.Scheme || endpoint.Host != copy.Host {
		return nil, nil, ErrInvalidRequest
	}
	client := config.Client
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true,
				MaxIdleConns: 50, MaxIdleConnsPerHost: 20, IdleConnTimeout: 60 * time.Second,
				TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: config.Timeout,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return endpoint, client, nil
}

func (provider *RazorpayProvider) Initialize(ctx context.Context, input ProviderInitialization) (ProviderSession, error) {
	if !validInitialization(input) || input.Amount.Currency != "INR" {
		return ProviderSession{}, ErrInvalidRequest
	}
	body, err := json.Marshal(map[string]any{
		"amount": input.Amount.AmountMinor, "currency": input.Amount.Currency,
		"receipt": input.OrderReference,
		"notes":   map[string]string{"planext4u_payment_id": input.PaymentID},
	})
	if err != nil {
		return ProviderSession{}, ErrProviderUnavailable
	}
	response, err := providerRequest(ctx, provider.client, provider.endpoint, provider.timeout, body, map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(provider.publicKey+":"+string(provider.secret))),
	})
	if err != nil {
		return ProviderSession{}, err
	}
	var payload struct {
		ID       string `json:"id"`
		Entity   string `json:"entity"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		Status   string `json:"status"`
	}
	if decodeProviderResponse(response, &payload) != nil || payload.Entity != "order" || payload.Amount != input.Amount.AmountMinor || payload.Currency != input.Amount.Currency ||
		(payload.Status != "created" && payload.Status != "attempted") || !safeID(payload.ID) {
		return ProviderSession{}, ErrProviderResponse
	}
	return ProviderSession{ProviderReference: payload.ID, ClientHandoff: ClientHandoff{
		Type: "RAZORPAY_CHECKOUT", PublicKey: provider.publicKey, ProviderOrderID: payload.ID,
	}}, nil
}

func (provider *PaystackProvider) Initialize(ctx context.Context, input ProviderInitialization) (ProviderSession, error) {
	if !validInitialization(input) || input.Amount.Currency != "NGN" || !validEmail(input.Payer.Email) {
		return ProviderSession{}, ErrInvalidRequest
	}
	body, err := json.Marshal(map[string]any{
		"email": input.Payer.Email, "amount": fmt.Sprintf("%d", input.Amount.AmountMinor),
		"currency": input.Amount.Currency, "reference": input.OrderReference,
		"metadata": map[string]string{"planext4u_payment_id": input.PaymentID},
	})
	if err != nil {
		return ProviderSession{}, ErrProviderUnavailable
	}
	response, err := providerRequest(ctx, provider.client, provider.endpoint, provider.timeout, body, map[string]string{
		"Authorization": "Bearer " + string(provider.secret),
	})
	if err != nil {
		return ProviderSession{}, err
	}
	var payload struct {
		Status bool `json:"status"`
		Data   struct {
			AuthorizationURL string `json:"authorization_url"`
			AccessCode       string `json:"access_code"`
			Reference        string `json:"reference"`
		} `json:"data"`
	}
	if decodeProviderResponse(response, &payload) != nil || !payload.Status || !safeID(payload.Data.Reference) {
		return ProviderSession{}, ErrProviderResponse
	}
	result := ProviderSession{ProviderReference: payload.Data.Reference, ClientHandoff: ClientHandoff{
		Type: "PAYSTACK_CHECKOUT", PublicKey: provider.publicKey, AccessCode: payload.Data.AccessCode, AuthorizationURL: payload.Data.AuthorizationURL,
	}}
	if !validProviderSession(MethodPaystack, result) {
		return ProviderSession{}, ErrProviderResponse
	}
	return result, nil
}

func (provider *RazorpayProvider) Verify(ctx context.Context, payment Payment) (ProviderVerification, error) {
	if payment.Method != MethodRazorpay || !safeID(payment.ProviderReference) || !safeID(payment.ProviderTransactionReference) {
		return ProviderVerification{}, ErrInvalidRequest
	}
	endpoint := provider.base.ResolveReference(&url.URL{Path: "/v1/payments/" + url.PathEscape(payment.ProviderTransactionReference)})
	response, err := providerHTTPRequest(ctx, provider.client, http.MethodGet, endpoint, provider.timeout, nil, map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(provider.publicKey+":"+string(provider.secret))),
	})
	if err != nil {
		return ProviderVerification{}, err
	}
	var payload struct {
		ID       string `json:"id"`
		OrderID  string `json:"order_id"`
		Status   string `json:"status"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	if decodeProviderResponse(response, &payload) != nil || payload.Status != "captured" || !safeID(payload.ID) || !safeID(payload.OrderID) {
		return ProviderVerification{}, ErrProviderResponse
	}
	return ProviderVerification{ProviderReference: payload.OrderID, ProviderTransactionReference: payload.ID, Status: StatusCaptured, Amount: Money{AmountMinor: payload.Amount, Currency: strings.ToUpper(payload.Currency)}}, nil
}

func (provider *PaystackProvider) Verify(ctx context.Context, payment Payment) (ProviderVerification, error) {
	if payment.Method != MethodPaystack || !safeID(payment.ProviderReference) {
		return ProviderVerification{}, ErrInvalidRequest
	}
	endpoint := provider.base.ResolveReference(&url.URL{Path: "/transaction/verify/" + url.PathEscape(payment.ProviderReference)})
	response, err := providerHTTPRequest(ctx, provider.client, http.MethodGet, endpoint, provider.timeout, nil, map[string]string{
		"Authorization": "Bearer " + string(provider.secret),
	})
	if err != nil {
		return ProviderVerification{}, err
	}
	var payload struct {
		Status bool `json:"status"`
		Data   struct {
			ID        json.Number `json:"id"`
			Reference string      `json:"reference"`
			Status    string      `json:"status"`
			Amount    int64       `json:"amount"`
			Currency  string      `json:"currency"`
		} `json:"data"`
	}
	if decodeProviderResponse(response, &payload) != nil || !payload.Status || payload.Data.Status != "success" || !safeID(payload.Data.Reference) || payload.Data.ID.String() == "" {
		return ProviderVerification{}, ErrProviderResponse
	}
	return ProviderVerification{ProviderReference: payload.Data.Reference, ProviderTransactionReference: "paystack-" + payload.Data.ID.String(), Status: StatusCaptured, Amount: Money{AmountMinor: payload.Data.Amount, Currency: strings.ToUpper(payload.Data.Currency)}}, nil
}

func (provider *RazorpayProvider) Refund(ctx context.Context, input ProviderRefundRequest) (ProviderRefundSubmission, error) {
	if !validRefundRequest(MethodRazorpay, input) {
		return ProviderRefundSubmission{}, ErrInvalidRequest
	}
	endpoint := provider.base.ResolveReference(&url.URL{Path: "/v1/payments/" + url.PathEscape(input.ProviderTransactionReference) + "/refund"})
	body, _ := json.Marshal(map[string]any{
		"amount": input.Amount.AmountMinor,
		"speed":  "normal",
		"notes":  map[string]string{"planext4u_payment_id": input.PaymentID, "reason": input.Reason},
	})
	response, err := providerHTTPRequest(ctx, provider.client, http.MethodPost, endpoint, provider.timeout, body, map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(provider.publicKey+":"+string(provider.secret))),
	})
	if err != nil {
		return ProviderRefundSubmission{}, err
	}
	var payload struct {
		ID        string `json:"id"`
		Entity    string `json:"entity"`
		PaymentID string `json:"payment_id"`
		Amount    int64  `json:"amount"`
		Currency  string `json:"currency"`
		Status    string `json:"status"`
	}
	if decodeProviderResponse(response, &payload) != nil || payload.Entity != "refund" || !safeID(payload.ID) ||
		payload.PaymentID != input.ProviderTransactionReference || payload.Amount != input.Amount.AmountMinor || strings.ToUpper(payload.Currency) != input.Amount.Currency {
		return ProviderRefundSubmission{}, ErrProviderResponse
	}
	status := StatusRefundSubmitted
	if payload.Status == "processed" {
		status = StatusRefunded
	} else if payload.Status != "pending" && payload.Status != "created" {
		return ProviderRefundSubmission{}, ErrProviderResponse
	}
	return ProviderRefundSubmission{ProviderRefundReference: payload.ID, Status: status}, nil
}

func (provider *PaystackProvider) Refund(ctx context.Context, input ProviderRefundRequest) (ProviderRefundSubmission, error) {
	if !validRefundRequest(MethodPaystack, input) {
		return ProviderRefundSubmission{}, ErrInvalidRequest
	}
	endpoint := provider.base.ResolveReference(&url.URL{Path: "/refund"})
	body, _ := json.Marshal(map[string]any{
		"transaction":   input.ProviderReference,
		"amount":        input.Amount.AmountMinor,
		"currency":      input.Amount.Currency,
		"customer_note": input.Reason,
		"merchant_note": "Planext4u payment " + input.PaymentID,
	})
	response, err := providerHTTPRequest(ctx, provider.client, http.MethodPost, endpoint, provider.timeout, body, map[string]string{
		"Authorization": "Bearer " + string(provider.secret),
	})
	if err != nil {
		return ProviderRefundSubmission{}, err
	}
	var payload struct {
		Status bool `json:"status"`
		Data   struct {
			ID          json.Number `json:"id"`
			Status      string      `json:"status"`
			Amount      int64       `json:"amount"`
			Currency    string      `json:"currency"`
			Transaction struct {
				Reference string `json:"reference"`
			} `json:"transaction"`
		} `json:"data"`
	}
	if decodeProviderResponse(response, &payload) != nil || !payload.Status || payload.Data.ID.String() == "" ||
		payload.Data.Transaction.Reference != input.ProviderReference || payload.Data.Amount != input.Amount.AmountMinor || strings.ToUpper(payload.Data.Currency) != input.Amount.Currency {
		return ProviderRefundSubmission{}, ErrProviderResponse
	}
	status := StatusRefundSubmitted
	if payload.Data.Status == "processed" {
		status = StatusRefunded
	} else if payload.Data.Status != "pending" && payload.Data.Status != "processing" {
		return ProviderRefundSubmission{}, ErrProviderResponse
	}
	return ProviderRefundSubmission{ProviderRefundReference: "paystack-refund-" + payload.Data.ID.String(), Status: status}, nil
}

func providerRequest(ctx context.Context, client *http.Client, endpoint *url.URL, timeout time.Duration, body []byte, headers map[string]string) ([]byte, error) {
	return providerHTTPRequest(ctx, client, http.MethodPost, endpoint, timeout, body, headers)
}

func providerHTTPRequest(ctx context.Context, client *http.Client, method string, endpoint *url.URL, timeout time.Duration, body []byte, headers map[string]string) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, ErrProviderUnavailable
	}
	request.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("initialize payment provider: %w", ErrProviderUnavailable)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBodyBytes+1))
	if err != nil || len(contents) > maxProviderBodyBytes {
		return nil, ErrProviderUnavailable
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, ErrProviderUnavailable
	}
	return contents, nil
}

func decodeProviderResponse(contents []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrProviderResponse
	}
	return nil
}

func validInitialization(value ProviderInitialization) bool {
	return safeID(value.PaymentID) && safeID(value.OrderReference) && validMoney(value.Amount)
}

func validRefundRequest(method Method, value ProviderRefundRequest) bool {
	if !safeID(value.PaymentID) || !safeID(value.ProviderReference) || !validMoney(value.Amount) ||
		value.Amount.AmountMinor < 1 || strings.TrimSpace(value.Reason) == "" || len(value.Reason) > 500 {
		return false
	}
	if method == MethodRazorpay {
		return value.Amount.Currency == "INR" && safeID(value.ProviderTransactionReference)
	}
	return method == MethodPaystack && value.Amount.Currency == "NGN"
}

func validEmail(value string) bool {
	if len(value) < 3 || len(value) > 254 || strings.TrimSpace(value) != value || strings.Count(value, "@") != 1 {
		return false
	}
	parts := strings.Split(value, "@")
	return parts[0] != "" && strings.Contains(parts[1], ".") && !strings.ContainsAny(value, "\r\n")
}

func isLoopbackHost(value string) bool {
	return value == "localhost" || value == "127.0.0.1" || value == "::1"
}
