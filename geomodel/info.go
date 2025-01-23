package geomodel

import (
	_ "unsafe"
)

type Info struct {
	Name        string `json:"name"`
	Street      string `json:"street"`
	HouseNumber string `json:"house_number"`
	City        string `json:"city"`
	Region      string `json:"region"`
}
