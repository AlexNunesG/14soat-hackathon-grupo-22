package domain

import (
	"fmt"
	"strings"
)

// FramePattern is the printf pattern of frame file names: 1-based, 4 digits,
// PNG (frame_0001.png, frame_0002.png, ...). ffmpeg understands it as an
// image sequence pattern.
const FramePattern = "frame_%04d.png"

// FrameName returns the file name of the n-th frame (1-based).
func FrameName(n int) string {
	return fmt.Sprintf(FramePattern, n)
}

// ZipName returns the download file name of a video's frames archive: the
// original name without its extension plus "_frames.zip"
// ("holiday.mp4" -> "holiday_frames.zip"). A name that is only an
// extension (".mp4") or empty falls back to "video_frames.zip".
func ZipName(originalName string) string {
	stem := originalName
	if i := strings.LastIndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	if stem == "" {
		stem = "video"
	}
	return stem + "_frames.zip"
}
