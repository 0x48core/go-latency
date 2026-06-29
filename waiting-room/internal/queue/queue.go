package queue

import "context"

type Queue interface {
	Enqueue(ctx context.Context, sessionID string) (position int64, err error)
	Position(ctx context.Context, sessionID string) (int64, error)
	Admit(ctx context.Context, n int64) ([]string, error)
	Size(ctx context.Context) (int64, error)
	Remove(ctx context.Context, sessionID string) error
}
