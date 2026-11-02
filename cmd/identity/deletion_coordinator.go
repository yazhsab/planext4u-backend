package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/identity"
)

func prepareDeletionTasks(ctx context.Context, repository *identity.PostgresRepository, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := repository.PrepareDeletionTasks(bounded, 100)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Error("deletion task preparation failed", "error", "bounded task sweep will retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
