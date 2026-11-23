package executor

import (
	"context"
	"log/slog"
)

// LevelTrace is the level used for full raw Gateway response bodies.
const LevelTrace = slog.LevelDebug - 4

func logAttrs(logger *slog.Logger, ctx context.Context, level slog.Level, message string, attrs ...slog.Attr) {
	if logger == nil {
		return
	}
	logger.LogAttrs(ctx, level, message, attrs...)
}
