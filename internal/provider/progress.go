package provider

import (
	"bytes"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// MaxProgressLine bounds one forwarded line.
//
// Backend output is text, but a mirror that serves a binary body instead of an
// image, or a download indicator written without newlines, would otherwise
// stream megabytes into the progress sink before the first line break.
const MaxProgressLine = 4096

// ProgressWriter forwards a backend's output to a types.ProgressSink one
// complete line at a time.
//
// Provisioning is the one slow path a user sits and waits through — it downloads
// an operating system — so its output has to be available live rather than only
// in a log file afterwards. Lines are emitted as types.ProgressLog events and
// the presentation layer decides whether verbose mode is on; nothing here writes
// to a terminal.
//
// It lives here rather than in either backend because both need exactly this,
// and the parts that are easy to get wrong are the parts that would be copied:
// the partial-line buffer, the bound that stops it growing without end, and the
// flush that recovers a failing tool's last word when it ended without a
// newline. Only the decoding differs, and that is a field.
//
// It is written to by one goroutine at a time: os/exec guarantees that when the
// same writer is given to a child's stdout and its stderr, at most one goroutine
// calls Write at a time, and deps.Runner's Stream does exactly that.
type ProgressWriter struct {
	// Machine is the environment the output belongs to.
	Machine string
	// Sink receives the lines. A nil Sink discards them.
	Sink types.ProgressSink
	// Decode turns one line's bytes into text. A nil Decode means the bytes
	// are already text, which is true of any backend whose tool writes UTF-8;
	// wsl.exe writes UTF-16 and passes its decoder here, because a line
	// forwarded raw would reach the user as NUL-separated letters.
	Decode func([]byte) string

	buf []byte
}

// Write splits the stream into lines and emits each complete one.
func (w *ProgressWriter) Write(p []byte) (int, error) {
	if w.Sink == nil {
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	for {
		newline := bytes.IndexByte(w.buf, '\n')
		if newline < 0 {
			break
		}
		w.emit(w.buf[:newline])
		w.buf = w.buf[newline+1:]
	}
	if len(w.buf) > MaxProgressLine {
		// A line this long is not a line. Emit what there is and start over so
		// the buffer cannot grow without bound.
		w.emit(w.buf[:MaxProgressLine])
		w.buf = w.buf[MaxProgressLine:]
	}
	return len(p), nil
}

// Flush emits whatever the backend wrote without a trailing newline, which is
// where a failing tool's last word tends to be.
func (w *ProgressWriter) Flush() {
	if w.Sink == nil || len(w.buf) == 0 {
		return
	}
	w.emit(w.buf)
	w.buf = nil
}

// emit forwards one line, dropping blank ones so that verbose output is not
// mostly empty.
func (w *ProgressWriter) emit(line []byte) {
	text := string(line)
	if w.Decode != nil {
		text = w.Decode(line)
	}
	// The trailing NUL is what a half-decoded UTF-16 line break leaves behind.
	text = strings.TrimRight(text, "\r\x00")
	if strings.TrimSpace(text) == "" {
		return
	}
	w.Sink.Progress(types.ProgressEvent{
		Kind:    types.ProgressLog,
		Machine: w.Machine,
		Message: text,
	})
}
