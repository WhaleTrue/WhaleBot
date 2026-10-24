// Package schemas contains the versioned, closed WhaleMesh wire contracts.
package schemas

import "embed"

// Files contains the complete local schema set; validation never fetches remote resources.
//
//go:embed *.schema.json
var Files embed.FS
