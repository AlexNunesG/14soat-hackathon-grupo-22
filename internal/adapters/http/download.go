package httpapi

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"video-processor/internal/app"
)

// downloadVideo handles GET /api/v1/videos/{id}/download: it streams the
// frames archive of a DONE video from the object storage.
func downloadVideo(log *slog.Logger, videos VideoService) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, obj, err := videos.Download(c.Request.Context(), currentUserID(c), c.Param("id"))
		var notReady *app.NotReadyError
		switch {
		case err == nil:
		case errors.Is(err, app.ErrNotFound):
			WriteError(c, http.StatusNotFound, CodeNotFound, "video not found")
			return
		case errors.As(err, &notReady):
			WriteError(c, http.StatusConflict, CodeVideoNotReady, notReady.Error())
			return
		default:
			writeAppError(c, log, err)
			return
		}
		defer obj.Close()

		h := c.Writer.Header()
		h.Set("Content-Type", "application/zip")
		h.Set("Content-Disposition", attachment(v.ZipName()))
		if obj.Size >= 0 {
			h.Set("Content-Length", strconv.FormatInt(obj.Size, 10))
		}
		c.Status(http.StatusOK)
		if _, err := io.Copy(c.Writer, obj); err != nil {
			// The status is already sent; the client sees a truncated body.
			log.WarnContext(c.Request.Context(), "download interrupted",
				slog.String("video_id", v.ID), slog.Any("error", err))
		}
	}
}

// attachment returns the Content-Disposition of a download named name
// (RFC 6266): attachment with filename="<name>", plus an RFC 5987
// filename* parameter (UTF-8, percent-encoded) when name is not plain
// ASCII, in which case filename holds an ASCII fallback with '_' for the
// other characters. Clients that understand filename* prefer it.
func attachment(name string) string {
	fallback, plain := asciiFallback(name)
	v := `attachment; filename="` + fallback + `"`
	if !plain {
		v += "; filename*=UTF-8''" + rfc5987Escape(name)
	}
	return v
}

// asciiFallback replaces what cannot appear in a quoted ASCII filename
// (non-ASCII, control characters, '"' and '\') with '_', and reports
// whether nothing was replaced.
func asciiFallback(name string) (string, bool) {
	var b strings.Builder
	plain := true
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			b.WriteByte('_')
			plain = false
			continue
		}
		b.WriteRune(r)
	}
	return b.String(), plain
}

// rfc5987Escape percent-encodes every byte but RFC 5987 attr-chars.
func rfc5987Escape(s string) string {
	const attrChars = "!#$&+-.^_`|~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || strings.IndexByte(attrChars, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
