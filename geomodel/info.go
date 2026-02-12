//go:generate go tool easyjson $GOFILE
package geomodel

import "github.com/paulmach/orb"

//easyjson:json
type InfoList []Info

//easyjson:json
type Info struct {
	Name        string `json:"name"`
	Street      string `json:"street"`
	HouseNumber string `json:"house_number"`
	City        string `json:"city"`
	Region      string `json:"region"`

	Weight uint8 `json:"weight"`
}

type Zone struct {
	Name    string
	Bounds  orb.Bound
	Polygon orb.MultiPolygon
}
