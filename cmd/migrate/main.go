package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yazhsab/planext4u-backend/internal/migrations"
)

func main() {
	root := flag.String("root", "migrations", "migration root")
	service := flag.String("service", "all", "owned service or all")
	direction := flag.String("direction", "up", "up or down")
	steps := flag.Int("steps", 1, "number of down migrations for one service")
	flag.Parse()
	if err := run(*root, *service, *direction, *steps); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root, service, direction string, steps int) error {
	secretPath := strings.TrimSpace(os.Getenv("MIGRATION_DATABASE_URL_FILE"))
	if secretPath == "" {
		return fmt.Errorf("MIGRATION_DATABASE_URL_FILE is required")
	}
	contents, err := os.ReadFile(secretPath)
	if err != nil {
		return fmt.Errorf("read migration database URL file: %w", err)
	}
	items, err := migrations.Discover(root)
	if err != nil {
		return err
	}
	if err := migrations.Validate(items); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	connection, err := pgx.Connect(ctx, strings.TrimSpace(string(contents)))
	if err != nil {
		return fmt.Errorf("connect migration database: %w", err)
	}
	defer connection.Close(ctx)
	runner, _ := migrations.NewRunner(connection)
	switch direction {
	case "up":
		return runner.Up(ctx, items, service)
	case "down":
		return runner.Down(ctx, items, service, steps)
	default:
		return fmt.Errorf("direction must be up or down")
	}
}
