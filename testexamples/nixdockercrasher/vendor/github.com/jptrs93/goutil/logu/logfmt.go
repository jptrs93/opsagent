package logu

import (
	"io"
	"log/slog"
)

func NewLogfmtLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(NewLogfmtHandler(w, level))
}

func NewLogfmtHandler(w io.Writer, level slog.Level) slog.Handler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
}
