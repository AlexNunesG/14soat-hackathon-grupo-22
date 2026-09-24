package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// DefaultMaxUploadBytes is the default limit of an upload request (1 GiB).
const DefaultMaxUploadBytes int64 = 1 << 30

// uploadField is the multipart field holding the videos.
const uploadField = "videos"

// UploadService is what the upload handler needs from the app
// (app.Uploads).
type UploadService interface {
	Upload(ctx context.Context, ownerID string, files []app.UploadFile) ([]*domain.Video, error)
}

// uploadBody is the UploadResponse schema.
type uploadBody struct {
	Videos []videoBody `json:"videos"`
}

// uploadVideos handles POST /api/v1/videos. It streams the multipart body
// part by part, spooling each file of the "videos" field to a temporary
// file (the whole request is never held in memory), and rejects the
// request as soon as a file name has an unsupported extension. Nothing is
// stored before every file has been received and checked. The temporary
// files are removed when the request ends.
func uploadVideos(log *slog.Logger, uploads UploadService, maxBytes int64, tempDir string, m *Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		spool := &fileSpool{dir: tempDir}
		defer func() { m.recordUpload(c, spool.files) }()
		if c.Request.ContentLength > maxBytes {
			payloadTooLarge(c, maxBytes)
			return
		}
		mr, ok := multipartReader(c.Request.Header.Get("Content-Type"), http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes))
		if !ok {
			missingFile(c)
			return
		}
		defer spool.remove(c.Request.Context(), log)

		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				badBody(c, err, maxBytes, len(spool.files) == 0)
				return
			}
			name := part.FileName()
			if part.FormName() != uploadField || name == "" {
				continue // not a file of the videos field; NextPart skips its content
			}
			if err := app.ValidateUpload(name); err != nil {
				WriteError(c, http.StatusBadRequest, CodeUnsupportedFormat, err.Error())
				return
			}
			if err := spool.add(name, part); err != nil {
				var readErr *partReadError
				if errors.As(err, &readErr) {
					badBody(c, readErr.err, maxBytes, false)
					return
				}
				writeAppError(c, log, err)
				return
			}
		}
		if len(spool.files) == 0 {
			missingFile(c)
			return
		}

		videos, err := uploads.Upload(c.Request.Context(), currentUserID(c), spool.files)
		switch {
		case err == nil:
		case errors.Is(err, app.ErrMissingFile):
			missingFile(c)
			return
		case errors.Is(err, domain.ErrUnsupportedFormat):
			WriteError(c, http.StatusBadRequest, CodeUnsupportedFormat, err.Error())
			return
		default:
			writeAppError(c, log, err)
			return
		}
		body := uploadBody{Videos: make([]videoBody, 0, len(videos))}
		for _, v := range videos {
			body.Videos = append(body.Videos, newVideoBody(v))
		}
		c.JSON(http.StatusAccepted, body)
	}
}

// multipartReader returns a reader of body when contentType is
// multipart/form-data with a boundary.
func multipartReader(contentType string, body io.Reader) (*multipart.Reader, bool) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, false
	}
	return multipart.NewReader(body, params["boundary"]), true
}

// badBody answers a body that could not be read: 413 when it is over the
// limit; otherwise 400, missing_file when no file was found yet (the body
// is not really multipart) and invalid_request for a broken one.
func badBody(c *gin.Context, err error, maxBytes int64, noFileYet bool) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		payloadTooLarge(c, maxBytes)
	case noFileYet:
		missingFile(c)
	default:
		WriteError(c, http.StatusBadRequest, CodeInvalidRequest, "malformed multipart body")
	}
}

func missingFile(c *gin.Context) {
	WriteError(c, http.StatusBadRequest, CodeMissingFile, `no file sent in multipart field "`+uploadField+`"`)
}

func payloadTooLarge(c *gin.Context, maxBytes int64) {
	WriteError(c, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
		fmt.Sprintf("request body exceeds the limit of %d bytes", maxBytes))
}

// partReadError is a failure to read an uploaded part (the client's fault),
// as opposed to a failure to write the temporary file (the server's).
type partReadError struct{ err error }

func (e *partReadError) Error() string { return "read upload: " + e.err.Error() }
func (e *partReadError) Unwrap() error { return e.err }

// fileSpool keeps the uploaded files in temporary files.
type fileSpool struct {
	dir   string
	files []app.UploadFile
	temps []*os.File
}

// add copies r to a new temporary file and records it as the upload of
// name, rewound for reading.
func (s *fileSpool) add(name string, r io.Reader) error {
	f, err := os.CreateTemp(s.dir, "upload-*")
	if err != nil {
		return fmt.Errorf("spool upload: %w", err)
	}
	s.temps = append(s.temps, f)
	n, err := io.Copy(f, readerOnly{r})
	if err != nil {
		var readErr *partReadError
		if errors.As(err, &readErr) {
			return err
		}
		return fmt.Errorf("spool upload: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("spool upload: %w", err)
	}
	s.files = append(s.files, app.UploadFile{Name: name, Size: n, Content: f})
	return nil
}

// remove closes and deletes the temporary files.
func (s *fileSpool) remove(ctx context.Context, log *slog.Logger) {
	for _, f := range s.temps {
		f.Close()
		if err := os.Remove(f.Name()); err != nil {
			log.WarnContext(ctx, "could not remove an upload's temporary file", slog.String("file", f.Name()), slog.Any("error", err))
		}
	}
}

// readerOnly tags the errors of the wrapped reader as *partReadError, so
// io.Copy's read and write failures can be told apart. It also hides any
// WriterTo of the part.
type readerOnly struct{ r io.Reader }

func (r readerOnly) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = &partReadError{err: err}
	}
	return n, err
}
