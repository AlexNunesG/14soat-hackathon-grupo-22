package domain

import (
	"errors"
	"fmt"
	"strings"
)

// supportedFormats lists the accepted upload extensions, in the order the
// contract (docs/openapi.yaml) prints them.
var supportedFormats = []string{"mp4", "avi", "mov", "mkv", "wmv", "flv", "webm"}

// ErrUnsupportedFormat is wrapped by the error ValidateFormat returns.
var ErrUnsupportedFormat = errors.New("unsupported file format")

// SupportedFormats returns the accepted upload extensions (lowercase,
// without the dot), in contract order. The slice is a copy.
func SupportedFormats() []string {
	return append([]string(nil), supportedFormats...)
}

// Extension returns the text after the last '.' of name, lowercased, and
// whether there is one. "clip" and "clip." have no extension.
func Extension(name string) (string, bool) {
	i := strings.LastIndexByte(name, '.')
	if i < 0 || i == len(name)-1 {
		return "", false
	}
	return strings.ToLower(name[i+1:]), true
}

// IsSupportedFormat reports whether name ends in a supported extension,
// case-insensitively. Only the last extension counts: "clip.mp4.exe" is not
// supported.
func IsSupportedFormat(name string) bool {
	ext, ok := Extension(name)
	if !ok {
		return false
	}
	for _, f := range supportedFormats {
		if ext == f {
			return true
		}
	}
	return false
}

// ValidateFormat returns nil when name has a supported extension, and
// otherwise an error wrapping ErrUnsupportedFormat whose message names the
// file and lists every supported format, e.g.
//
//	unsupported file format "notes.txt"; supported formats: mp4, avi, mov, mkv, wmv, flv, webm
func ValidateFormat(name string) error {
	if IsSupportedFormat(name) {
		return nil
	}
	return fmt.Errorf("%w %q; supported formats: %s",
		ErrUnsupportedFormat, name, strings.Join(supportedFormats, ", "))
}
