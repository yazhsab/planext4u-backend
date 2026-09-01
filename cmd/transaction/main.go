package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/booking"
	"github.com/yazhsab/planext4u-backend/internal/checkout"
	"github.com/yazhsab/planext4u-backend/internal/commerce"
	"github.com/yazhsab/planext4u-backend/internal/emergency"
	"github.com/yazhsab/planext4u-backend/internal/food"
	"github.com/yazhsab/planext4u-backend/internal/fulfillment"
	"github.com/yazhsab/planext4u-backend/internal/inventory"
	"github.com/yazhsab/planext4u-backend/internal/localverticals"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	platformconfig "github.com/yazhsab/planext4u-backend/internal/platform/config"
	"github.com/yazhsab/planext4u-backend/internal/platform/logging"
	"github.com/yazhsab/planext4u-backend/internal/platform/server"
	"github.com/yazhsab/planext4u-backend/internal/platform/telemetry"
	"github.com/yazhsab/planext4u-backend/internal/social"
	"github.com/yazhsab/planext4u-backend/internal/supply"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

var version = "dev"

type providerRuntime struct {
	publicKey     string
	apiSecretFile string
	webhookFile   string
	baseURL       *url.URL
}

type runtimeConfig struct {
	service                platformconfig.Config
	databaseURLFile        string
	databaseURL            string
	databaseMaxConns       int32
	databaseMinConns       int32
	databaseMaxLifetime    time.Duration
	tenantID               string
	countries              []string
	walletProgramFile      string
	bookingOTPKeyFile      string
	emergencyDataKeyFile   string
	localContactKeyFile    string
	rewardPolicy           wallet.RewardPolicy
	providerTimeout        time.Duration
	razorpay               providerRuntime
	paystack               providerRuntime
	notificationURL        *url.URL
	notificationSecretFile string
	workerInterval         time.Duration
	reconciliationAge      time.Duration
}

type walletProgramFile struct {
	ReferralBaseURL string `json:"referral_base_url"`
	RefillOffers    []struct {
		ID             string       `json:"id"`
		Country        string       `json:"country"`
		Points         int64        `json:"points"`
		BonusPoints    int64        `json:"bonus_points"`
		Price          wallet.Money `json:"price"`
		PaymentMethods []string     `json:"payment_methods"`
		ExpiresAfter   string       `json:"expires_after"`
	} `json:"refill_offers"`
	Campaigns []wallet.RewardCampaign `json:"campaigns"`
}

func main() { os.Exit(run()) }

func run() int {
	runtime, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid transaction service settings", "error", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, runtime.service.LogLevel)
	if err != nil {
		slog.Error("invalid logging configuration", "error", err)
		return 2
	}
	databaseURL, err := loadDatabaseURL(runtime)
	if err != nil {
		logger.Error("load transaction database configuration", "error", "database secret is unavailable or invalid")
		return 2
	}
	poolConfig, err := databaseConfig(databaseURL, runtime)
	if err != nil {
		logger.Error("configure transaction database", "error", err)
		return 2
	}
	startup, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStartup()
	pool, err := pgxpool.NewWithConfig(startup, poolConfig)
	if err != nil {
		logger.Error("create transaction database pool", "error", "database configuration is invalid")
		return 2
	}
	defer pool.Close()
	if err := pool.Ping(startup); err != nil {
		logger.Error("connect transaction database", "error", "database is unavailable")
		return 1
	}

	providerSecrets, providers, err := paymentProviders(runtime)
	if err != nil {
		logger.Error("configure payment providers", "error", err)
		return 2
	}
	program, err := loadWalletProgram(runtime.walletProgramFile)
	if err != nil {
		logger.Error("load wallet program", "error", err)
		return 2
	}
	catalogProvider, _ := commerce.NewCatalogSnapshotProvider(pool)
	cartService, err := commerce.NewPostgresService(pool, catalogProvider, time.Now)
	if err != nil {
		logger.Error("configure durable cart service", "error", err)
		return 2
	}
	inventoryService, err := inventory.NewPostgresService(pool, time.Now)
	if err != nil {
		logger.Error("configure durable inventory service", "error", err)
		return 2
	}
	paymentService, err := payment.NewPostgresService(pool, time.Now, providerSecrets, providers)
	if err != nil {
		logger.Error("configure durable payment service", "error", err)
		return 2
	}
	walletService, err := wallet.NewPostgresService(pool, time.Now, runtime.rewardPolicy, program)
	if err != nil {
		logger.Error("configure durable wallet service", "error", err)
		return 2
	}
	bookingOTPKey, err := readSecret(runtime.bookingOTPKeyFile, 4096)
	if err != nil || len(bookingOTPKey) != 32 {
		logger.Error("load booking OTP encryption key", "error", "booking OTP key is unavailable or invalid")
		return 2
	}
	bookingService, err := booking.NewPostgresService(pool, paymentService, walletService, time.Now, bookingOTPKey)
	if err != nil {
		logger.Error("configure durable booking service", "error", err)
		return 2
	}
	supplyService, err := supply.NewPostgresService(pool, time.Now)
	if err != nil {
		logger.Error("configure durable vendor supply service", "error", err)
		return 2
	}
	foodService, err := food.NewPostgresService(pool, paymentService, walletService, time.Now)
	if err != nil {
		logger.Error("configure durable food service", "error", err)
		return 2
	}
	fulfillmentService, err := fulfillment.NewPostgresService(pool, time.Now)
	if err != nil {
		logger.Error("configure durable fulfillment service", "error", err)
		return 2
	}
	emergencyDataKey, err := readSecret(runtime.emergencyDataKeyFile, 4096)
	if err != nil || len(emergencyDataKey) != 32 {
		logger.Error("load emergency data encryption key", "error", "emergency data key is unavailable or invalid")
		return 2
	}
	emergencyService, err := emergency.NewPostgresService(pool, time.Now, emergencyDataKey)
	if err != nil {
		logger.Error("configure durable emergency service", "error", err)
		return 2
	}
	localContactKey, err := readSecret(runtime.localContactKeyFile, 4096)
	if err != nil || len(localContactKey) != 32 {
		logger.Error("load local marketplace contact encryption key", "error", "local contact key is unavailable or invalid")
		return 2
	}
	localVerticalService, err := localverticals.NewPostgresService(pool, time.Now, localContactKey)
	if err != nil {
		logger.Error("configure durable homes and classifieds service", "error", err)
		return 2
	}
	socialService, err := social.NewPostgresService(pool, time.Now, walletService)
	if err != nil {
		logger.Error("configure durable social service", "error", err)
		return 2
	}

	var notifier order.Notifier
	if runtime.notificationURL != nil {
		key, keyErr := readSecret(runtime.notificationSecretFile, 4096)
		if keyErr != nil {
			logger.Error("load notification authentication key", "error", "notification authentication key is unavailable or invalid")
			return 2
		}
		notifier, err = checkout.NewHTTPOrderNotifier(checkout.HTTPOrderNotifierConfig{BaseURL: runtime.notificationURL, Secret: key, Timeout: runtime.providerTimeout})
		if err != nil {
			logger.Error("configure order notification client", "error", err)
			return 2
		}
	}
	orderService, err := order.NewPostgresServiceWithNotifier(pool, time.Now, notifier)
	if err != nil {
		logger.Error("configure durable order service", "error", err)
		return 2
	}
	checkoutStore, _ := checkout.NewPostgresStore(pool, time.Now)
	configuration, _ := checkout.NewPostgresConfigurationProvider(pool)
	commercialTerms, _ := checkout.NewPostgresCommercialTermsResolver(pool)
	payers, _ := checkout.NewPostgresPayerResolver(pool)
	checkoutService, err := checkout.NewDynamicPersistentService(checkout.Dependencies{
		Cart: cartService, Inventory: inventoryService, Wallet: walletService, Payment: paymentService,
		Orders: orderService, Payers: payers, CommercialTerms: commercialTerms,
	}, configuration, checkoutStore, time.Now)
	if err != nil {
		logger.Error("configure checkout orchestration", "error", err)
		return 2
	}
	cartHandler, _ := commerce.NewHandler(cartService)
	checkoutHandler, _ := checkout.NewHandler(checkoutService)
	bookingHandler, _ := booking.NewHandler(bookingService)
	supplyHandler, _ := supply.NewHandler(supplyService)
	foodHandler, _ := food.NewHandler(foodService)
	fulfillmentHandler, _ := fulfillment.NewHandler(fulfillmentService)
	emergencyHandler, _ := emergency.NewHandler(emergencyService)
	localVerticalHandler, _ := localverticals.NewHandler(localVerticalService)
	socialHandler, _ := social.NewHandler(socialService)
	application := http.NewServeMux()
	application.Handle("/v1/cart", cartHandler)
	application.Handle("/v1/cart/", cartHandler)
	for _, prefix := range []string{"/v1/services", "/v1/services/", "/v1/service-slot-holds", "/v1/service-slot-holds/", "/v1/service-bookings", "/v1/service-bookings/"} {
		application.Handle(prefix, bookingHandler)
	}
	application.Handle("/v1/vendor/", supplyHandler)
	for _, prefix := range []string{"/v1/restaurants", "/v1/restaurants/", "/v1/food-carts", "/v1/food-orders", "/v1/food-orders/"} {
		application.Handle(prefix, foodHandler)
	}
	for _, prefix := range []string{"/v1/rider/", "/v1/dispatch/", "/v1/order-chats/", "/v1/settlements/", "/v1/payouts", "/v1/payouts/", "/v1/operations/"} {
		application.Handle(prefix, fulfillmentHandler)
	}
	application.Handle("/v1/emergency/", emergencyHandler)
	application.Handle("/v1/homes/", localVerticalHandler)
	application.Handle("/v1/classifieds/", localVerticalHandler)
	application.Handle("/v1/social/", socialHandler)
	application.Handle("/v1/moderation/", socialHandler)
	application.Handle("/", checkoutHandler)

	dependenciesReady := func(ctx context.Context) error {
		checks := []func(context.Context) error{
			pool.Ping, catalogProvider.Ready, cartService.Ready, inventoryService.Ready,
			paymentService.Ready, walletService.Ready, bookingService.Ready, supplyService.Ready, foodService.Ready, fulfillmentService.Ready, emergencyService.Ready, localVerticalService.Ready, socialService.Ready, orderService.Ready, checkoutStore.Ready,
			configuration.Ready, commercialTerms.Ready, payers.Ready,
		}
		for _, check := range checks {
			if err := check(ctx); err != nil {
				return err
			}
		}
		for _, country := range runtime.countries {
			if _, err := configuration.Configuration(ctx, checkout.Scope{TenantID: runtime.tenantID, Country: country, CustomerID: "00000000-0000-0000-0000-000000000000"}); err != nil {
				return fmt.Errorf("published checkout configuration for %s: %w", country, err)
			}
		}
		return nil
	}
	if err := dependenciesReady(startup); err != nil {
		logger.Error("check transaction dependencies", "error", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go recoveryLoop(ctx, logger, inventoryService, paymentService, orderService, bookingService, foodService, emergencyService, localVerticalService, socialService, checkoutService, notifier != nil, runtime)
	traceRatio := 1.0
	if runtime.service.Environment == platformconfig.EnvironmentProduction {
		traceRatio = 0.10
	}
	observability, err := telemetry.Setup(ctx, telemetry.Config{ServiceName: runtime.service.ServiceName, ServiceVersion: version, Environment: string(runtime.service.Environment), TraceRatio: traceRatio})
	if err != nil {
		logger.Error("initialize telemetry", "error", err)
		return 1
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), runtime.service.ShutdownTimeout)
		defer cancel()
		if err := observability.Shutdown(shutdown); err != nil {
			logger.Warn("flush telemetry", "error", err)
		}
	}()
	httpServer := server.New(runtime.service, logger, version, server.WithTelemetry(observability), server.WithApplication(application, transactionRoute), server.WithReadiness(dependenciesReady))
	logger.Info("transaction service starting", "environment", runtime.service.Environment, "address", runtime.service.HTTPAddress)
	if err := httpServer.Serve(ctx); err != nil {
		logger.Error("transaction service stopped unexpectedly", "error", err)
		return 1
	}
	logger.Info("transaction service stopped")
	return 0
}

func recoveryLoop(ctx context.Context, logger *slog.Logger, stock *inventory.PostgresService, payments *payment.PostgresService, orders *order.PostgresService, bookings *booking.PostgresService, foodOrders *food.PostgresService, emergencies *emergency.PostgresService, localMarkets *localverticals.PostgresService, socialNetwork *social.PostgresService, checkoutService *checkout.Service, notifications bool, runtime runtimeConfig) {
	ticker := time.NewTicker(runtime.workerInterval)
	defer ticker.Stop()
	for {
		if released := stock.Expire(); released > 0 {
			logger.Info("released expired inventory reservations", "count", released)
		}
		if expired, err := bookings.Expire(ctx, 100); err != nil && ctx.Err() == nil {
			logger.Warn("expire booking holds and payments", "error", err)
		} else if expired > 0 {
			logger.Info("expired booking payment windows", "count", expired)
		}
		if expired, err := foodOrders.Expire(ctx, 100); err != nil && ctx.Err() == nil {
			logger.Warn("expire restaurant acceptance windows", "error", err)
		} else if expired > 0 {
			logger.Info("expired restaurant acceptance windows", "count", expired)
		}
		for _, country := range runtime.countries {
			systemActor := emergency.Actor{TenantID: runtime.tenantID, Country: country, Subject: "00000000-0000-4000-8000-000000000001", Roles: []string{"EMERGENCY_ADMIN"}, MFAVerified: true}
			if escalated, err := emergencies.RunEscalations(systemActor); err != nil && ctx.Err() == nil {
				logger.Warn("run emergency escalations", "country", country, "error", err)
			} else if escalated > 0 {
				logger.Warn("escalated emergency requests", "country", country, "count", escalated)
			}
			retentionActor := localverticals.Actor{TenantID: runtime.tenantID, Country: country, Subject: "00000000-0000-4000-8000-000000000002", Roles: []string{"CONTENT_ADMIN"}, MFAVerified: true}
			if expired, err := localMarkets.ExpireClassifieds(retentionActor); err != nil && ctx.Err() == nil {
				logger.Warn("expire classified listings", "country", country, "error", err)
			} else if expired > 0 {
				logger.Info("expired classified listings", "country", country, "count", expired)
			}
			socialActor := social.Actor{TenantID: runtime.tenantID, Country: country, Subject: "00000000-0000-4000-8000-000000000003", Roles: []string{"CONTENT_ADMIN"}, MFAVerified: true}
			if purged, err := socialNetwork.PurgeExpired(socialActor); err != nil && ctx.Err() == nil {
				logger.Warn("purge expired social content", "country", country, "error", err)
			} else if purged > 0 {
				logger.Info("purged expired social content", "country", country, "count", purged)
			}
		}
		if rewarded, err := socialNetwork.ProcessRewards(ctx, 100); err != nil && ctx.Err() == nil {
			logger.Warn("deliver social engagement rewards", "error", err)
		} else if rewarded > 0 {
			logger.Info("delivered social engagement rewards", "count", rewarded)
		}
		if notifications {
			if _, err := orders.ProcessNotifications(ctx, 100); err != nil && ctx.Err() == nil {
				logger.Warn("process order notifications", "error", err)
			}
		}
		candidates, err := payments.ReconciliationCandidates(ctx, runtime.reconciliationAge, 100)
		if err != nil && ctx.Err() == nil {
			logger.Warn("list payment reconciliation candidates", "error", err)
		}
		for _, candidate := range candidates {
			value, reconcileErr := payments.ReconcileWithProvider(ctx, candidate.Scope, candidate.PaymentID)
			if reconcileErr != nil {
				if !errors.Is(reconcileErr, payment.ErrProviderUnavailable) && !errors.Is(reconcileErr, payment.ErrReconciliation) && ctx.Err() == nil {
					logger.Warn("reconcile payment", "payment_id", candidate.PaymentID, "error", reconcileErr)
				}
				continue
			}
			if _, _, finalizeErr := checkoutService.FinalizeProviderPayment("reconcile-"+value.ID, value.ID); finalizeErr != nil && ctx.Err() == nil {
				logger.Warn("finalize reconciled payment", "payment_id", value.ID, "error", finalizeErr)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func paymentProviders(runtime runtimeConfig) (map[payment.Method][]byte, map[payment.Method]payment.ProviderInitializer, error) {
	secrets := map[payment.Method][]byte{}
	providers := map[payment.Method]payment.ProviderInitializer{}
	for method, value := range map[payment.Method]providerRuntime{payment.MethodRazorpay: runtime.razorpay, payment.MethodPaystack: runtime.paystack} {
		if value.publicKey == "" {
			continue
		}
		apiSecret, err := readSecret(value.apiSecretFile, 4096)
		if err != nil {
			return nil, nil, fmt.Errorf("%s API secret is unavailable or invalid", method)
		}
		webhookSecret, err := readSecret(value.webhookFile, 4096)
		if err != nil || len(webhookSecret) < 32 {
			return nil, nil, fmt.Errorf("%s webhook secret is unavailable or invalid", method)
		}
		config := payment.HTTPProviderConfig{BaseURL: value.baseURL, PublicKey: value.publicKey, Secret: apiSecret, Timeout: runtime.providerTimeout}
		var provider payment.ProviderInitializer
		if method == payment.MethodRazorpay {
			provider, err = payment.NewRazorpayProvider(config)
		} else {
			provider, err = payment.NewPaystackProvider(config)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("configure %s provider: %w", method, err)
		}
		secrets[method], providers[method] = webhookSecret, provider
	}
	return secrets, providers, nil
}

func loadWalletProgram(path string) (wallet.Program, error) {
	if path == "" {
		return wallet.Program{}, nil
	}
	contents, err := readRegularFile(path, 256*1024)
	if err != nil {
		return wallet.Program{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	var input walletProgramFile
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return wallet.Program{}, errors.New("wallet program is invalid")
	}
	result := wallet.Program{ReferralBaseURL: input.ReferralBaseURL, Campaigns: input.Campaigns}
	for _, value := range input.RefillOffers {
		duration, err := time.ParseDuration(value.ExpiresAfter)
		if err != nil {
			return wallet.Program{}, errors.New("wallet refill expiry is invalid")
		}
		result.RefillOffers = append(result.RefillOffers, wallet.RefillOffer{ID: value.ID, Country: value.Country, Points: value.Points, BonusPoints: value.BonusPoints, Price: value.Price, PaymentMethods: value.PaymentMethods, ExpiresAfter: duration})
	}
	return result, nil
}

func loadRuntimeConfig(lookup func(string) (string, bool)) (runtimeConfig, error) {
	wrapped := func(key string) (string, bool) {
		switch key {
		case "SERVICE_NAME":
			return valueOrDefault(lookup, key, "planext4u-transaction"), true
		case "HTTP_ADDRESS":
			return valueOrDefault(lookup, key, ":8086"), true
		default:
			return lookup(key)
		}
	}
	base, err := platformconfig.LoadFrom(wrapped)
	if err != nil {
		return runtimeConfig{}, err
	}
	result := runtimeConfig{
		service: base, databaseURLFile: requiredValue(lookup, "DATABASE_URL_FILE"), databaseURL: requiredValue(lookup, "DATABASE_URL"),
		databaseMaxConns: 100, databaseMinConns: 10, databaseMaxLifetime: 30 * time.Minute,
		tenantID: requiredValue(lookup, "TENANT_ID"), countries: splitValues(valueOrDefault(lookup, "SUPPORTED_COUNTRIES", "IN")),
		walletProgramFile: requiredValue(lookup, "WALLET_PROGRAM_FILE"), bookingOTPKeyFile: requiredValue(lookup, "BOOKING_OTP_KEY_FILE"), emergencyDataKeyFile: requiredValue(lookup, "EMERGENCY_DATA_KEY_FILE"), localContactKeyFile: requiredValue(lookup, "LOCAL_VERTICALS_CONTACT_KEY_FILE"), providerTimeout: 15 * time.Second,
		workerInterval: 5 * time.Second, reconciliationAge: 2 * time.Minute,
		rewardPolicy:           wallet.RewardPolicy{DailyDeviceCap: 100, Cooldown: time.Minute, ReferralSenderPoints: 100, ReferralRecipientPoints: 50, ReferralExpiry: 365 * 24 * time.Hour},
		notificationSecretFile: requiredValue(lookup, "ORDER_NOTIFICATION_HMAC_KEY_FILE"),
	}
	result.razorpay, err = loadProviderRuntime(lookup, "RAZORPAY", "RAZORPAY_KEY_ID")
	if err != nil {
		return runtimeConfig{}, err
	}
	result.paystack, err = loadProviderRuntime(lookup, "PAYSTACK", "PAYSTACK_PUBLIC_KEY")
	if err != nil {
		return runtimeConfig{}, err
	}
	if raw := requiredValue(lookup, "NOTIFICATION_BASE_URL"); raw != "" {
		result.notificationURL, err = parseServiceURL(raw, base.Environment)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("NOTIFICATION_BASE_URL is invalid")
		}
	}
	for key, destination := range map[string]*time.Duration{"PROVIDER_TIMEOUT": &result.providerTimeout, "DATABASE_MAX_LIFETIME": &result.databaseMaxLifetime, "WORKER_INTERVAL": &result.workerInterval, "RECONCILIATION_AGE": &result.reconciliationAge, "REWARD_COOLDOWN": &result.rewardPolicy.Cooldown, "REFERRAL_EXPIRY": &result.rewardPolicy.ReferralExpiry} {
		if raw := requiredValue(lookup, key); raw != "" {
			*destination, err = time.ParseDuration(raw)
			if err != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be a duration", key)
			}
		}
	}
	for key, destination := range map[string]*int64{"REWARD_DAILY_DEVICE_CAP": &result.rewardPolicy.DailyDeviceCap, "REFERRAL_SENDER_POINTS": &result.rewardPolicy.ReferralSenderPoints, "REFERRAL_RECIPIENT_POINTS": &result.rewardPolicy.ReferralRecipientPoints} {
		if raw := requiredValue(lookup, key); raw != "" {
			*destination, err = strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be an integer", key)
			}
		}
	}
	for key, destination := range map[string]*int32{"DATABASE_MAX_CONNS": &result.databaseMaxConns, "DATABASE_MIN_CONNS": &result.databaseMinConns} {
		if raw := requiredValue(lookup, key); raw != "" {
			parsed, parseErr := strconv.ParseInt(raw, 10, 32)
			if parseErr != nil {
				return runtimeConfig{}, fmt.Errorf("%s must be an integer", key)
			}
			*destination = int32(parsed)
		}
	}
	if (result.databaseURLFile == "") == (result.databaseURL == "") || uuid.Validate(result.tenantID) != nil || len(result.countries) == 0 || result.bookingOTPKeyFile == "" || result.emergencyDataKeyFile == "" || result.localContactKeyFile == "" ||
		result.databaseMaxConns < 1 || result.databaseMaxConns > 500 || result.databaseMinConns < 0 || result.databaseMinConns > result.databaseMaxConns ||
		result.databaseMaxLifetime < time.Minute || result.databaseMaxLifetime > 24*time.Hour || result.providerTimeout < time.Second || result.providerTimeout > 30*time.Second ||
		result.workerInterval < time.Second || result.workerInterval > time.Minute || result.reconciliationAge < time.Minute || result.reconciliationAge > 24*time.Hour ||
		result.rewardPolicy.DailyDeviceCap < 0 || result.rewardPolicy.Cooldown < 0 || result.rewardPolicy.ReferralSenderPoints < 0 || result.rewardPolicy.ReferralRecipientPoints < 0 || result.rewardPolicy.ReferralExpiry < 0 {
		return runtimeConfig{}, errors.New("required transaction settings are missing or outside their safe range")
	}
	for _, country := range result.countries {
		if len(country) != 2 || country != strings.ToUpper(country) {
			return runtimeConfig{}, errors.New("SUPPORTED_COUNTRIES must contain uppercase ISO alpha-2 codes")
		}
		if base.Environment != platformconfig.EnvironmentDevelopment && (country == "IN" && result.razorpay.publicKey == "" || country == "NG" && result.paystack.publicKey == "") {
			return runtimeConfig{}, fmt.Errorf("the payment provider for %s is required", country)
		}
	}
	if base.Environment != platformconfig.EnvironmentDevelopment && (result.walletProgramFile == "" || result.notificationURL == nil || result.notificationSecretFile == "") {
		return runtimeConfig{}, errors.New("wallet program and notification authentication are required outside development")
	}
	return result, nil
}

func loadProviderRuntime(lookup func(string) (string, bool), prefix, publicKeyName string) (providerRuntime, error) {
	result := providerRuntime{publicKey: requiredValue(lookup, publicKeyName), apiSecretFile: requiredValue(lookup, prefix+"_API_SECRET_FILE"), webhookFile: requiredValue(lookup, prefix+"_WEBHOOK_SECRET_FILE")}
	if result.publicKey == "" && result.apiSecretFile == "" && result.webhookFile == "" {
		return result, nil
	}
	if result.publicKey == "" || result.apiSecretFile == "" || result.webhookFile == "" {
		return providerRuntime{}, fmt.Errorf("%s provider settings must be supplied together", prefix)
	}
	if raw := requiredValue(lookup, prefix+"_BASE_URL"); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil {
			return providerRuntime{}, fmt.Errorf("%s_BASE_URL is invalid", prefix)
		}
		result.baseURL = parsed
	}
	return result, nil
}

func databaseConfig(value string, runtime runtimeConfig) (*pgxpool.Config, error) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return nil, errors.New("database URL is invalid")
	}
	if runtime.service.Environment != platformconfig.EnvironmentDevelopment && parsed.Query().Get("sslmode") != "verify-full" {
		return nil, errors.New("database URL must use sslmode=verify-full outside development")
	}
	config, err := pgxpool.ParseConfig(value)
	if err != nil {
		return nil, errors.New("database URL is invalid")
	}
	if runtime.service.Environment != platformconfig.EnvironmentDevelopment && config.ConnConfig.TLSConfig == nil {
		return nil, errors.New("database URL must require TLS outside development")
	}
	config.MaxConns, config.MinConns, config.MaxConnLifetime = runtime.databaseMaxConns, runtime.databaseMinConns, runtime.databaseMaxLifetime
	config.MaxConnIdleTime, config.HealthCheckPeriod = 5*time.Minute, 30*time.Second
	return config, nil
}

func loadDatabaseURL(runtime runtimeConfig) (string, error) {
	if runtime.databaseURL != "" {
		return runtime.databaseURL, nil
	}
	contents, err := readRegularFile(runtime.databaseURLFile, 4096)
	return strings.TrimSpace(string(contents)), err
}

func readSecret(path string, limit int64) ([]byte, error) {
	contents, err := readRegularFile(path, limit)
	if err != nil {
		return nil, err
	}
	contents = []byte(strings.TrimSpace(string(contents)))
	if len(contents) < 16 {
		return nil, errors.New("secret is too short")
	}
	return contents, nil
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("file is invalid")
	}
	return io.ReadAll(io.LimitReader(file, limit))
}

func parseServiceURL(raw string, environment platformconfig.Environment) (*url.URL, error) {
	value, err := url.Parse(raw)
	privateHTTP := value != nil && value.Scheme == "http" &&
		(!strings.Contains(value.Hostname(), ".") || strings.HasSuffix(strings.ToLower(value.Hostname()), ".internal"))
	if err != nil || !value.IsAbs() || value.Host == "" || value.User != nil || value.RawQuery != "" || value.Fragment != "" ||
		(value.Scheme != "https" && !(environment == platformconfig.EnvironmentDevelopment && value.Scheme == "http") && !privateHTTP) {
		return nil, errors.New("service URL is invalid")
	}
	return value, nil
}

func transactionRoute(request *http.Request) string {
	path := request.URL.Path
	switch {
	case path == "/v1/cart":
		return "/v1/cart"
	case strings.HasPrefix(path, "/v1/cart/items/"):
		return "/v1/cart/items/{variant_id}"
	case path == "/v1/addresses", path == "/v1/delivery-slots", path == "/v1/checkout/quotes", path == "/v1/checkout/orders", path == "/v1/orders", path == "/v1/wallet", path == "/v1/wallet/experience", path == "/v1/wallet/referrals", path == "/v1/wallet/refills":
		return path
	case strings.HasPrefix(path, "/v1/addresses/"):
		return "/v1/addresses/{address_id}"
	case strings.HasPrefix(path, "/v1/payments/webhooks/"):
		return "/v1/payments/webhooks/{provider}"
	case strings.HasPrefix(path, "/v1/payments/"):
		return "/v1/payments/{payment_id}"
	case strings.HasPrefix(path, "/v1/orders/"):
		return "/v1/orders/{order_id}"
	case path == "/v1/services":
		return path
	case strings.HasPrefix(path, "/v1/services/") && strings.HasSuffix(path, "/slots"):
		return "/v1/services/{service_id}/slots"
	case strings.HasPrefix(path, "/v1/services/"):
		return "/v1/services/{service_id}"
	case path == "/v1/service-slot-holds":
		return path
	case strings.HasPrefix(path, "/v1/service-slot-holds/"):
		return "/v1/service-slot-holds/{hold_id}"
	case path == "/v1/service-bookings":
		return path
	case strings.HasPrefix(path, "/v1/service-bookings/"):
		return "/v1/service-bookings/{booking_id}/action"
	case strings.HasPrefix(path, "/v1/vendor/"):
		return "/v1/vendor/{resource}"
	case path == "/v1/restaurants", path == "/v1/food-carts", path == "/v1/food-orders":
		return path
	case strings.HasPrefix(path, "/v1/restaurants/"):
		return "/v1/restaurants/{restaurant_id}/menu"
	case strings.HasPrefix(path, "/v1/food-orders/"):
		return "/v1/food-orders/{order_id}/action"
	case strings.HasPrefix(path, "/v1/rider/applications/"):
		return "/v1/rider/applications/{rider_id}/review"
	case path == "/v1/rider/applications", path == "/v1/rider/profile", path == "/v1/rider/duty", path == "/v1/rider/duty/start", path == "/v1/rider/duty/end", path == "/v1/rider/offers", path == "/v1/rider/tasks", path == "/v1/rider/location", path == "/v1/rider/offline-recovery":
		return path
	case strings.HasPrefix(path, "/v1/rider/tasks/"):
		return "/v1/rider/tasks/{task_id}/action"
	case strings.HasPrefix(path, "/v1/rider/locations/"):
		return "/v1/rider/locations/{rider_id}"
	case path == "/v1/dispatch/tasks", path == "/v1/dispatch/stale-assignment-sweep":
		return path
	case strings.HasPrefix(path, "/v1/dispatch/tasks/"):
		return "/v1/dispatch/tasks/{task_id}/action"
	case strings.HasPrefix(path, "/v1/order-chats/"):
		return "/v1/order-chats/{resource}"
	case path == "/v1/settlements/ledger", path == "/v1/settlements/seed", path == "/v1/settlements/reconciliation", path == "/v1/payouts":
		return path
	case strings.HasPrefix(path, "/v1/payouts/"):
		return "/v1/payouts/{payout_id}/action"
	case strings.HasPrefix(path, "/v1/operations/"):
		return "/v1/operations/{resource}"
	case strings.HasPrefix(path, "/v1/emergency/"):
		return "/v1/emergency/{resource}"
	case strings.HasPrefix(path, "/v1/homes/"):
		return "/v1/homes/{resource}"
	case strings.HasPrefix(path, "/v1/classifieds/"):
		return "/v1/classifieds/{resource}"
	case strings.HasPrefix(path, "/v1/social/"):
		return "/v1/social/{resource}"
	case strings.HasPrefix(path, "/v1/moderation/"):
		return "/v1/moderation/{resource}"
	default:
		return "unmatched"
	}
}

func requiredValue(lookup func(string) (string, bool), key string) string {
	value, _ := lookup(key)
	return strings.TrimSpace(value)
}

func valueOrDefault(lookup func(string) (string, bool), key, fallback string) string {
	if value := requiredValue(lookup, key); value != "" {
		return value
	}
	return fallback
}

func splitValues(value string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" && !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}
