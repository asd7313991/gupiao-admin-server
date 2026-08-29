package news

import (
	"context"
	"errors"
)

var ErrRetryable = errors.New("retryable fetch error")

type SourceReader interface {
	GetID() uint
	GetName() string
	GetBaseURL() string
	GetCategoryMappingRaw() string
	GetConfigJSONRaw() string
	GetTimeoutSeconds() int
}

type Adapter interface {
	Key() string
	Fetch(ctx context.Context, source SourceReader, cfg SourceConfig) ([]NormalizedNews, error)
}
