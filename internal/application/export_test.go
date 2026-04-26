package application

import "context"

// ProcessBatchForTest exposes the unexported processBatch helper to the
// external application_test package. The external-package layout is
// required because internal/mocks now imports application (for
// application.Repositories), and a same-package test importing mocks
// would create a compile-time cycle. This file is in *_test.go so it
// never compiles into the production binary.
func (r *OutboxRelay) ProcessBatchForTest(ctx context.Context) {
	r.processBatch(ctx)
}
