// Package web embeds the browser assets shared by the Go and Rust servers.
package web

import _ "embed"

// FGeoDemo is the self-contained forward geocoding demo page served by both
// servers at GET /fgeocode/demo.
//
//go:embed fgeocode-demo.html
var FGeoDemo []byte
