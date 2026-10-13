package httpapi

import "net/http"

// Swagger UI is served by the API itself, under the same origin as the
// endpoints it describes: "Try it out" then needs no CORS. Its assets come from
// a CDN at a pinned version; the OpenAPI file is embedded in the binary.
const (
	pathDocs         = "/docs"
	pathDocsSpec     = "/docs/openapi.yaml"
	swaggerUIVersion = "5.17.14"
	swaggerUIAssets  = "https://cdn.jsdelivr.net/npm/swagger-ui-dist@" + swaggerUIVersion
)

const docsPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Coin transfer service — API</title>
<link rel="stylesheet" href="` + swaggerUIAssets + `/swagger-ui.css">
</head>
<body>
<div id="swagger-ui"></div>
<script src="` + swaggerUIAssets + `/swagger-ui-bundle.js"></script>
<script>
window.ui = SwaggerUIBundle({ url: "` + pathDocsSpec + `", dom_id: "#swagger-ui" });
</script>
</body>
</html>
`

// docsHandlers serve Swagger UI and the OpenAPI file it shows.
type docsHandlers struct {
	spec    []byte
	respond responder
}

func (d docsHandlers) page(w http.ResponseWriter, _ *http.Request) {
	d.respond.write(w, http.StatusOK, "text/html; charset=utf-8", []byte(docsPage))
}

func (d docsHandlers) openAPI(w http.ResponseWriter, _ *http.Request) {
	d.respond.write(w, http.StatusOK, "application/yaml", d.spec)
}
