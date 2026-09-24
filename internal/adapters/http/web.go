package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// webFS holds the web UI (web/): one page and its script and stylesheet,
// with no build step and no external assets.
//
//go:embed web
var webFS embed.FS

// UIPrefix is where the web UI's static assets are served.
const UIPrefix = "/ui/"

// contentSecurityPolicy allows only same-origin scripts, styles and API
// calls, plus the blob: URLs of downloads and the page's data: favicon.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

// mountWebUI serves the page at GET / and its assets under UIPrefix. Every
// other unknown path, /api/* included, still gets the JSON 404 envelope.
func mountWebUI(r *gin.Engine) {
	assets, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	page, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(assets)

	r.GET("/", func(c *gin.Context) {
		uiHeaders(c)
		c.Data(http.StatusOK, "text/html; charset=utf-8", page)
	})
	r.GET(UIPrefix+"*file", func(c *gin.Context) {
		name := c.Param("file")
		// Only the files themselves: no directory listings, and the page
		// has its own route.
		if info, err := fs.Stat(assets, strings.TrimPrefix(name, "/")); err != nil || info.IsDir() || name == "/index.html" {
			WriteError(c, http.StatusNotFound, CodeNotFound, "route not found")
			return
		}
		uiHeaders(c)
		req := c.Request.Clone(c.Request.Context())
		req.URL.Path = name
		files.ServeHTTP(c.Writer, req)
	})
}

// uiHeaders sets the security and caching headers of the web UI. no-cache
// makes browsers revalidate, so a new release is picked up at once.
func uiHeaders(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-cache")
}
