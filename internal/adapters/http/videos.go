package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// VideoService is what the video handlers need from the app (app.Videos).
type VideoService interface {
	List(ctx context.Context, ownerID string, page app.Page) (app.VideoPage, error)
	Get(ctx context.Context, ownerID, id string) (*domain.Video, error)
}

// videoBody is the Video schema. frame_count and error_message are null
// unless set; download_url is present only when DONE.
type videoBody struct {
	ID           string  `json:"id"`
	OriginalName string  `json:"original_name"`
	Status       string  `json:"status"`
	FrameCount   *int    `json:"frame_count"`
	ErrorMessage *string `json:"error_message"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	DownloadURL  string  `json:"download_url,omitempty"`
}

// videoPageBody is the VideoPage schema.
type videoPageBody struct {
	Items    []videoBody `json:"items"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Total    int         `json:"total"`
}

// DownloadPath is the download route of a video.
func DownloadPath(id string) string { return "/api/v1/videos/" + id + "/download" }

func newVideoBody(v *domain.Video) videoBody {
	b := videoBody{
		ID:           v.ID,
		OriginalName: v.OriginalName,
		Status:       v.Status.String(),
		CreatedAt:    formatTime(v.CreatedAt),
		UpdatedAt:    formatTime(v.UpdatedAt),
	}
	if v.FrameCount > 0 {
		n := v.FrameCount
		b.FrameCount = &n
	}
	if v.ErrorMessage != "" {
		msg := v.ErrorMessage
		b.ErrorMessage = &msg
	}
	if v.Status == domain.StatusDone {
		b.DownloadURL = DownloadPath(v.ID)
	}
	return b
}

// listVideos handles GET /api/v1/videos.
func listVideos(log *slog.Logger, videos VideoService) gin.HandlerFunc {
	return func(c *gin.Context) {
		number, ok := intQuery(c, "page", 1, "must be an integer >= 1")
		if !ok {
			return
		}
		size, ok := intQuery(c, "page_size", app.DefaultPageSize, "must be an integer between 1 and 100")
		if !ok {
			return
		}
		page, err := videos.List(c.Request.Context(), currentUserID(c), app.Page{Number: number, Size: size})
		if err != nil {
			writeAppError(c, log, err)
			return
		}
		body := videoPageBody{
			Items:    make([]videoBody, 0, len(page.Items)),
			Page:     page.Page.Number,
			PageSize: page.Page.Size,
			Total:    page.Total,
		}
		for i := range page.Items {
			body.Items = append(body.Items, newVideoBody(&page.Items[i]))
		}
		c.JSON(http.StatusOK, body)
	}
}

// intQuery reads an optional integer query parameter. A present but
// non-integer value answers 400 invalid_request and returns false.
func intQuery(c *gin.Context, name string, def int, rule string) (int, bool) {
	raw, present := c.GetQuery(name)
	if !present {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		WriteError(c, http.StatusBadRequest, CodeInvalidRequest, name+": "+rule)
		return 0, false
	}
	return n, true
}

// getVideo handles GET /api/v1/videos/{id}.
func getVideo(log *slog.Logger, videos VideoService) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, err := videos.Get(c.Request.Context(), currentUserID(c), c.Param("id"))
		switch {
		case err == nil:
			c.JSON(http.StatusOK, newVideoBody(v))
		case errors.Is(err, app.ErrNotFound):
			WriteError(c, http.StatusNotFound, CodeNotFound, "video not found")
		default:
			writeAppError(c, log, err)
		}
	}
}

// notImplemented answers routes whose behavior arrives in a later phase
// (upload: PLAN.md 2.3, download: 2.4). They already sit behind the auth
// middleware.
func notImplemented(c *gin.Context) {
	WriteError(c, http.StatusNotImplemented, CodeInternal, "not implemented yet")
}
