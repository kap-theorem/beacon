package dlq

import (
	"log/slog"

	"go.temporal.io/sdk/client"
)

// DLQService provides query and replay operations over failed Temporal workflow executions.
type DLQService struct {
	tc        client.Client
	namespace string
	logger    *slog.Logger
}

func NewDLQService(tc client.Client, namespace string, logger *slog.Logger) *DLQService {
	return &DLQService{tc: tc, namespace: namespace, logger: logger}
}
