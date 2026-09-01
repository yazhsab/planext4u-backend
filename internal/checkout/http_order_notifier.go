package checkout

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/order"
)

type HTTPOrderNotifierConfig struct {
	BaseURL *url.URL
	Secret  []byte
	Client  *http.Client
	Timeout time.Duration
}

type HTTPOrderNotifier struct {
	endpoint *url.URL
	secret   []byte
	client   *http.Client
	timeout  time.Duration
}

func NewHTTPOrderNotifier(config HTTPOrderNotifierConfig) (*HTTPOrderNotifier, error) {
	if config.BaseURL == nil || !config.BaseURL.IsAbs() || config.BaseURL.Host == "" || config.BaseURL.User != nil || config.BaseURL.RawQuery != "" || config.BaseURL.Fragment != "" ||
		(config.BaseURL.Scheme != "https" && !(config.BaseURL.Scheme == "http" && loopbackHost(config.BaseURL.Hostname()))) ||
		len(config.Secret) < 32 || len(config.Secret) > 4096 || config.Timeout <= 0 || config.Timeout > 30*time.Second {
		return nil, ErrInvalidRequest
	}
	base := *config.BaseURL
	endpoint := base.ResolveReference(&url.URL{Path: "/internal/v1/order-notifications"})
	if endpoint.Scheme != base.Scheme || endpoint.Host != base.Host {
		return nil, ErrInvalidRequest
	}
	client := config.Client
	if client == nil {
		client = &http.Client{
			Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true, MaxIdleConns: 20, MaxIdleConnsPerHost: 10, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: config.Timeout},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &HTTPOrderNotifier{endpoint: endpoint, secret: append([]byte(nil), config.Secret...), client: client, timeout: config.Timeout}, nil
}

func (notifier *HTTPOrderNotifier) Send(parent context.Context, value order.Notification) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) > 32*1024 {
		return order.ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(parent, notifier.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, notifier.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, notifier.secret)
	_, _ = mac.Write(body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Planext4u-Internal-Signature", hex.EncodeToString(mac.Sum(nil)))
	response, err := notifier.client.Do(request)
	if err != nil {
		return fmt.Errorf("send order notification: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 16*1024))
	if response.StatusCode != http.StatusNoContent {
		return errors.New("notification service rejected the order notification")
	}
	return nil
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
