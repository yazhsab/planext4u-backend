package messaging

import (
	"context"
	"time"
)

// TransactionalOutbox is implemented by a service-owned database adapter.
// The supplied context carries that adapter's transaction, so the domain
// mutation and staged outbox insert commit or roll back together.
type TransactionalOutbox interface {
	WithinTransaction(context.Context, func(context.Context, TransactionalWriter) error) error
}

type TransactionalWriter interface {
	Enqueue(context.Context, Message) error
}

func (service *Service) EnqueueAtomic(ctx context.Context, unit TransactionalOutbox, message Message, mutate func(context.Context) error) error {
	if unit == nil || mutate == nil || !validMessage(message) {
		return ErrInvalidRequest
	}
	return unit.WithinTransaction(ctx, func(txContext context.Context, writer TransactionalWriter) error {
		if err := mutate(txContext); err != nil {
			return err
		}
		return writer.Enqueue(txContext, cloneMessage(message))
	})
}

type MemoryUnitOfWork struct {
	store *MemoryStore
	now   func() time.Time
}

func NewMemoryUnitOfWork(store *MemoryStore, clock func() time.Time) *MemoryUnitOfWork {
	return &MemoryUnitOfWork{store: store, now: clock}
}

func (unit *MemoryUnitOfWork) WithinTransaction(ctx context.Context, operation func(context.Context, TransactionalWriter) error) error {
	staged := &stagedWriter{}
	if err := operation(ctx, staged); err != nil {
		return err
	}
	for _, message := range staged.messages {
		if err := unit.store.Enqueue(ctx, message, unit.now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

type stagedWriter struct{ messages []Message }

func (writer *stagedWriter) Enqueue(_ context.Context, message Message) error {
	if !validMessage(message) {
		return ErrInvalidRequest
	}
	writer.messages = append(writer.messages, cloneMessage(message))
	return nil
}
