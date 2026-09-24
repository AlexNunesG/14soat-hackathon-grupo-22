package integration

// Integration tests for POST /api/v1/videos (docs/openapi.yaml,
// operationId uploadVideos). Uploads only validate the file extension; what
// happens to the content afterwards is covered by processing_test.go.

import (
	"net/http"
	"strings"
	"testing"
)

func TestUploadSingleVideoIsAccepted(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)

	// mustUpload checks the 202, one video per file with a UUID id, the
	// original_name and status PENDING.
	v := uploadOne(t, token, "holiday.mp4", makeMP4(t, 1))

	// The created video is visible to its owner right away.
	if got := getVideo(t, token, v.ID); got.OriginalName != "holiday.mp4" {
		t.Errorf("GET the uploaded video: original_name = %q, want %q", got.OriginalName, "holiday.mp4")
	}
}

func TestUploadSeveralVideosInOneRequest(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	data := makeMP4(t, 1)
	files := []namedFile{{"first.mp4", data}, {"Second.MKV", data}, {"third.webm", data}}

	videos := mustUpload(t, token, files...)

	seen := map[string]bool{}
	for _, v := range videos {
		if seen[v.ID] {
			t.Errorf("id %s is used by more than one video", v.ID)
		}
		seen[v.ID] = true
	}
	if page := listVideos(t, token, ""); page.Total != len(files) {
		t.Errorf("list total = %d, want %d", page.Total, len(files))
	}
}

func TestUploadWithoutFileIsRejected(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	data := makeMP4(t, 1)

	cases := []struct {
		name        string
		contentType string
		body        string
	}{
		{"multipart without parts", "multipart/form-data; boundary=XyZ", "--XyZ--\r\n"},
		{"file in another field", "multipart/form-data; boundary=XyZ",
			"--XyZ\r\nContent-Disposition: form-data; name=\"file\"; filename=\"clip.mp4\"\r\n" +
				"Content-Type: application/octet-stream\r\n\r\n" + string(data) + "\r\n--XyZ--\r\n"},
		{"videos field without a file", "multipart/form-data; boundary=XyZ",
			"--XyZ\r\nContent-Disposition: form-data; name=\"videos\"\r\n\r\n\r\n--XyZ--\r\n"},
		{"non-multipart body", "application/json", `{"videos": ["clip.mp4"]}`},
	}
	t.Run("cases", func(t *testing.T) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				resp, body := doRequest(t, http.MethodPost, "/api/v1/videos", token, tc.contentType, strings.NewReader(tc.body))
				assertError(t, resp, body, http.StatusBadRequest, "missing_file")
			})
		}
	})
	assertNoVideos(t, token)
}

func TestUploadUnsupportedExtensionIsRejected(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	data := makeMP4(t, 1)

	for _, name := range []string{
		"notes.txt",
		"animation.gif",
		"clip.mpeg",
		"clip.m4v",
		"clip.3gp",
		"clip",
		"mp4",
		"clip.",
		"clip.mp4.exe",
		"clip.mp4.zip",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, token := registerAndLogin(t)
			resp, body := uploadVideos(t, token, namedFile{name, data})
			assertUnsupportedFormat(t, resp, body)
			assertNoVideos(t, token)
		})
	}

	// The request is all-or-nothing: one bad file rejects the valid ones too.
	for name, files := range map[string][]namedFile{
		"valid then unsupported": {{"good.mp4", data}, {"bad.txt", data}},
		"unsupported then valid": {{"bad.txt", data}, {"good.mp4", data}},
		"unsupported among many": {{"a.mp4", data}, {"b.mkv", data}, {"c.exe", data}, {"d.webm", data}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, token := registerAndLogin(t)
			resp, body := uploadVideos(t, token, files...)
			assertUnsupportedFormat(t, resp, body)
			assertNoVideos(t, token)
		})
	}
}

// assertUnsupportedFormat checks a 400 unsupported_format whose message
// lists every supported format, as the contract promises.
func assertUnsupportedFormat(t *testing.T, resp *http.Response, body []byte) {
	t.Helper()
	assertError(t, resp, body, http.StatusBadRequest, "unsupported_format")
	msg := strings.ToLower(errorMessage(t, body))
	for _, format := range supportedFormats {
		if !strings.Contains(msg, format) {
			t.Errorf("message %q does not list the supported format %q", msg, format)
		}
	}
}

func TestUploadSupportedFormatsAreAcceptedInAnyCase(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	// Only the extension is validated at upload time, so any bytes will do.
	data := makeMP4(t, 1)

	names := []string{"clip.MP4", "clip.WebM", "clip.Mkv", "clip.AVI", "my.holiday.mov", "vídeo com espaços.mp4"}
	for _, format := range supportedFormats {
		names = append(names, "clip."+format)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			uploadOne(t, token, name, data)
		})
	}
}
