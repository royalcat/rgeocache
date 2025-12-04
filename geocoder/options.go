package geocoder

import "log/slog"

const defautMaxSearchRadius float64 = 0.01

func loadOptions(opts ...Option) options {
	options := options{
		searchRadius: defautMaxSearchRadius,
		logger:       slog.Default(),
	}
	for _, o := range opts {
		o.apply(&options)
	}
	return options
}

type options struct {
	searchRadius float64
	logger       *slog.Logger
}

type Option interface {
	apply(*options)
}

type searchRadiusOption float64

func (r searchRadiusOption) apply(o *options) {
	o.searchRadius = float64(r)
}

// Default: 0.1
func WithSearchRadius(radius float64) Option {
	return searchRadiusOption(radius)
}

type loggerOption struct {
	logger *slog.Logger
}

func (l loggerOption) apply(o *options) {
	o.logger = l.logger
}

// Default: nil
func WithLogger(logger *slog.Logger) Option {
	return loggerOption{logger}
}
