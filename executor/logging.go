package executor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// LevelTrace is the level used for full raw Gateway response bodies.
const LevelTrace = slog.LevelDebug - 4

// ParseLogLevel parses the executor's supported text log level.
func ParseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (want trace, debug, info, warn, or error)", value)
	}
}

// NewTextLogger returns a structured text logger that labels LevelTrace as TRACE.
func NewTextLogger(output io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.LevelKey && attr.Value.Kind() == slog.KindAny {
				if actualLevel, ok := attr.Value.Any().(slog.Level); ok && actualLevel == LevelTrace {
					return slog.String(slog.LevelKey, "TRACE")
				}
			}
			return attr
		},
	}))
}

func logAttrs(logger *slog.Logger, ctx context.Context, level slog.Level, message string, attrs ...slog.Attr) {
	if logger == nil {
		return
	}
	logger.LogAttrs(ctx, level, message, attrs...)
}
