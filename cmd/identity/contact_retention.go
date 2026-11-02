package main

import (
	"context"
	"github.com/yazhsab/planext4u-backend/internal/identity"
	"log/slog"
	"time"
)

func expireContacts(ctx context.Context, repository *identity.PostgresRepository, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := repository.ExpireContacts(bounded, time.Now().UTC(), 100)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Error("contact retention batch failed", "error", "retention will retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
