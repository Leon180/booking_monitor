package domain

import "context"

//go:generate mockgen -source=uow.go -destination=../mocks/uow_mock.go -package=mocks
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}
