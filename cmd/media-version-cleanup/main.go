// Command media-version-cleanup performs one explicitly selected, due identity
// document version cleanup. It is intentionally separate from the media server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/media"
)

const cleanupLogin = "planext4u_media_cleanup_login"

type cleanupConfig struct {
	databaseURL string
	accessKey   string
	secretKey   string
	region      string
	bucket      string
	endpoint    *url.URL
	requestID   string
	assetID     string
}

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	config, err := loadConfig(args, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "media cleanup configuration is invalid:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(config.databaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "media cleanup database configuration is invalid")
		return 2
	}
	poolConfig.MinConns, poolConfig.MaxConns = 0, 1
	poolConfig.AfterConnect = assumeCleanupRole
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		fmt.Fprintln(stderr, "media cleanup database is unavailable")
		return 1
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintln(stderr, "media cleanup database login or role is unavailable")
		return 1
	}
	configuration, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(config.region),
		awsconfig.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(config.accessKey, config.secretKey, "")),
	)
	if err != nil {
		fmt.Fprintln(stderr, "media cleanup object-store configuration is unavailable")
		return 1
	}
	client := s3.NewFromConfig(configuration, func(options *s3.Options) {
		if config.endpoint != nil {
			options.BaseEndpoint = aws.String(config.endpoint.String())
			options.UsePathStyle = true
		}
	})
	cleaner, err := media.NewDueVersionCleaner(pool, client, config.bucket)
	if err != nil {
		fmt.Fprintln(stderr, "media cleanup scope is invalid")
		return 2
	}
	result, err := cleaner.PurgeDueAsset(ctx, config.requestID, config.assetID)
	if err != nil {
		// SQL/provider errors can contain private object keys and endpoints.
		fmt.Fprintln(stderr, "media cleanup did not complete; the exact asset remains retryable")
		return 1
	}
	if result.AlreadyPurged {
		fmt.Fprintln(stdout, "media cleanup already recorded for this asset")
	} else {
		fmt.Fprintf(stdout, "media cleanup recorded: %d versions and %d markers removed\n", result.VersionsDeleted, result.MarkersDeleted)
	}
	return 0
}

func loadConfig(args []string, getenv func(string) string) (cleanupConfig, error) {
	var config cleanupConfig
	flags := flag.NewFlagSet("media-version-cleanup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var execute bool
	flags.StringVar(&config.requestID, "request-id", "", "exact due deletion request")
	flags.StringVar(&config.assetID, "asset-id", "", "exact media asset UUID")
	flags.BoolVar(&execute, "execute", false, "authorize this one version purge")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !execute ||
		!regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`).MatchString(config.requestID) ||
		uuid.Validate(config.assetID) != nil {
		return config, errors.New("exact request ID, asset UUID and --execute are required")
	}
	config.region = strings.TrimSpace(getenv("MEDIA_CLEANUP_S3_REGION"))
	config.bucket = strings.TrimSpace(getenv("MEDIA_CLEANUP_S3_BUCKET"))
	if config.region == "" || config.bucket == "" {
		return config, errors.New("dedicated S3 region and bucket are required")
	}
	var err error
	config.databaseURL, err = readSecret(getenv("MEDIA_CLEANUP_DATABASE_URL_FILE"), 4096)
	if err != nil {
		return config, errors.New("dedicated database URL file is unavailable")
	}
	parsed, err := url.Parse(config.databaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Host == "" || parsed.User == nil || parsed.User.Username() != cleanupLogin ||
		parsed.Query().Get("sslmode") != "verify-full" {
		return config, errors.New("dedicated database login with verified TLS is required")
	}
	config.accessKey, err = readSecret(getenv("MEDIA_CLEANUP_S3_ACCESS_KEY_FILE"), 1024)
	if err != nil || len(config.accessKey) < 3 {
		return config, errors.New("dedicated S3 access key file is unavailable")
	}
	config.secretKey, err = readSecret(getenv("MEDIA_CLEANUP_S3_SECRET_KEY_FILE"), 4096)
	if err != nil || len(config.secretKey) < 8 {
		return config, errors.New("dedicated S3 secret key file is unavailable")
	}
	if raw := strings.TrimSpace(getenv("MEDIA_CLEANUP_S3_ENDPOINT")); raw != "" {
		config.endpoint, err = url.Parse(raw)
		if err != nil || config.endpoint.Host == "" || config.endpoint.User != nil ||
			config.endpoint.RawQuery != "" || config.endpoint.Fragment != "" ||
			(config.endpoint.Path != "" && config.endpoint.Path != "/") ||
			(config.endpoint.Scheme != "https" && !(config.endpoint.Scheme == "http" && loopback(config.endpoint.Hostname()))) {
			return config, errors.New("S3 endpoint must use HTTPS or loopback HTTP")
		}
	}
	return config, nil
}

func readSecret(path string, max int64) (string, error) {
	if path == "" {
		return "", errors.New("missing secret file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > max {
		return "", errors.New("secret file is invalid")
	}
	bytes, err := io.ReadAll(io.LimitReader(file, max))
	return strings.TrimSpace(string(bytes)), err
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func assumeCleanupRole(ctx context.Context, conn *pgx.Conn) error {
	var login, current string
	var canLogin, superuser, createRole, createDB, replication, bypassRLS bool
	var safeCleanupRole bool
	var otherMemberships int
	err := conn.QueryRow(ctx, `SELECT session_user,current_user,r.rolcanlogin,r.rolsuper,
	 r.rolcreaterole,r.rolcreatedb,r.rolreplication,r.rolbypassrls,
	 (SELECT count(*) FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid
	  WHERE (m.member=r.oid AND granted.rolname<>'planext4u_media_cleanup')
	     OR m.member=(SELECT oid FROM pg_roles WHERE rolname='planext4u_media_cleanup')),
	 EXISTS (SELECT 1 FROM pg_roles c WHERE c.rolname='planext4u_media_cleanup'
	  AND NOT c.rolcanlogin AND NOT c.rolsuper AND NOT c.rolcreaterole
	  AND NOT c.rolcreatedb AND NOT c.rolreplication AND NOT c.rolbypassrls)
	 FROM pg_roles r WHERE r.rolname=session_user`).Scan(&login, &current, &canLogin,
		&superuser, &createRole, &createDB, &replication, &bypassRLS, &otherMemberships, &safeCleanupRole)
	if err != nil || login != cleanupLogin || current != login || !canLogin || superuser ||
		createRole || createDB || replication || bypassRLS || otherMemberships != 0 || !safeCleanupRole {
		return errors.New("dedicated media cleanup database login is required")
	}
	if _, err := conn.Exec(ctx, `SET ROLE planext4u_media_cleanup`); err != nil {
		return errors.New("media cleanup role is unavailable")
	}
	if err := conn.QueryRow(ctx, `SELECT current_user`).Scan(&current); err != nil || current != "planext4u_media_cleanup" {
		return errors.New("media cleanup role was not assumed")
	}
	return nil
}
