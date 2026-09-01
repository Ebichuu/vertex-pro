package migrations

import "embed"

// FS contains the ordered SQL migrations shipped with Vertex Pro.
//
//go:embed *.sql
var FS embed.FS
