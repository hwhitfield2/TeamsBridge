package connector

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"teamsbridge.local/teamsbridge/internal/graph"
)

func TestQuietConversionKeepsRetryCauseWithoutNotice(t *testing.T) {
	cause := &graph.APIError{Status: 429, Code: "throttled"}
	err := quietConversionError(context.Background(), cause)
	var apiErr *graph.APIError
	if !errors.Is(err, bridgev2.ErrIgnoringRemoteEvent) || !errors.As(err, &apiErr) || apiErr.Status != 429 {
		t.Fatal("retry cause or notice suppression was lost")
	}
	if quietConversionError(context.Background(), nil) != nil {
		t.Fatal("successful conversion changed")
	}
}
