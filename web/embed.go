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
