package geocoder

type options struct {
	searchRadius float64
}

type Option interface {
	apply(*options)
}

type searchRadius float64

func (r searchRadius) apply(o *options) {
	o.searchRadius = float64(r)
}

// Default: 0.1
func WithSearchRadius(radius float64) Option {
	return searchRadius(radius)
}
