package connector

import (
	"context"
	"errors"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"teamsbridge.local/teamsbridge/internal/graph"
)

// bridgev2 sends a new Matrix notice for every conversion failure, even when
// message_error_notices is disabled. Keep retries and diagnostics without
// generating user-visible messages on every poll. queueMessage still requires
// matching durable database state before it acknowledges delivery.
func quietConversionError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	zerolog.Ctx(ctx).Warn().Err(err).Msg("Teams conversion incomplete; will retry without posting an error notice")
	return errors.Join(bridgev2.ErrIgnoringRemoteEvent, err)
}
func (c *Client) convertMessageQuietly(ctx context.Context, p *bridgev2.Portal, intent bridgev2.MatrixAPI, m graph.Message) (*bridgev2.ConvertedMessage, error) {
	out, err := c.convertMessage(ctx, p, intent, m)
	return out, quietConversionError(ctx, err)
}
func (c *Client) convertEditQuietly(ctx context.Context, p *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, m graph.Message) (*bridgev2.ConvertedEdit, error) {
	out, err := c.convertEdit(ctx, p, intent, existing, m)
	return out, quietConversionError(ctx, err)
}
