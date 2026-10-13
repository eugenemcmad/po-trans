// Package docs embeds the OpenAPI description of the service, so that the
// binary can serve it (and Swagger UI on top of it) without files on disk.
package docs

import _ "embed"

// OpenAPI is docs/openapi.yaml, the hand-written OpenAPI 3.0.3 description.
//
//go:embed openapi.yaml
var OpenAPI []byte
