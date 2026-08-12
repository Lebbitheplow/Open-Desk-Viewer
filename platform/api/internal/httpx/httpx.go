package httpx

import (
	"github.com/rs/zerolog"
)

// Options holds HTTP middleware options
type Options struct {
	RequestIDHeader string
	LogLevel        zerolog.Level
}

// DefaultOptions returns default middleware options
func DefaultOptions() Options {
	return Options{
		RequestIDHeader: "X-Request-ID",
		LogLevel:        zerolog.InfoLevel,
	}
}
