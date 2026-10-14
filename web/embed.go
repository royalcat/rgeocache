// Package web embeds the browser assets shared by the Go and Rust servers.
package web

import _ "embed"

// FGeoDemo is the self-contained forward geocoding demo page served by both
// servers at GET /fgeocode/demo.
//
// The Rust server embeds a copy at server_rs/web/fgeocode-demo.html, because
// its Docker build context (server_rs/) cannot reach this file; keep the two
// in sync (the Rust test demo_page_copy_matches_shared_page enforces it).
//
//go:embed fgeocode-demo.html
var FGeoDemo []byte

// RGeoDemo is the self-contained reverse geocoding demo page served by both
// servers at GET /rgeocode/demo: an interactive map of the cache points around
// a clicked location, via GET /rgeocode/points.
//
// The Rust server embeds a copy at server_rs/web/rgeocode-demo.html for the
// same Docker build context reason as FGeoDemo; keep the two in sync (the Rust
// test rgeocode_demo_page_copy_matches_shared_page enforces it).
//
//go:embed rgeocode-demo.html
var RGeoDemo []byte
