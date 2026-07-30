package lima

import (
	"bytes"

	"github.com/olamide226/avar/internal/types"
)

// maxProgressLine bounds one forwarded line. Lima's provisioning output is
// text, but a mirror that serves a binary body instead of an image would
// otherwise stream megabytes into the progress sink before the first newline.
const maxProgressLine = 4096

// progressLines forwards backend output to a ProgressSink one complete line at a
// time.
//
// Provisioning is the one slow path a user sits and waits through, so its output
// has to be available live rather than only in the log file afterwards. Lines
// are emitted as ProgressLog events and the presentation layer decides whether
// verbose mode is on; nothing here writes to a terminal.
//
// It is written to by one goroutine at a time: os/exec guarantees that when the
// same writer is used for both a child's stdout and its stderr, at most one
// goroutine calls Write at a time, and deps.Runner's Stream does exactly that.
type progressLines struct {
	machine string
	sink    types.ProgressSink
	buf     []byte
}

// Write splits the stream into lines and emits each complete one.
func (w *progressLines) Write(p []byte) (int, error) {
	if w.sink == nil {
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
	if len(w.buf) > maxProgressLine {
		// A line this long is not a line. Emit what there is and start over so
		// the buffer cannot grow without bound.
		w.emit(w.buf[:maxProgressLine])
		w.buf = w.buf[maxProgressLine:]
	}
	return len(p), nil
}

// flush emits whatever the backend wrote without a trailing newline, which is
// where a failing tool's last word tends to be.
func (w *progressLines) flush() {
	if w.sink == nil || len(w.buf) == 0 {
		return
	}
	w.emit(w.buf)
	w.buf = nil
}

// emit forwards one line, dropping blank ones so that verbose output is not
// mostly empty.
func (w *progressLines) emit(line []byte) {
	text := string(bytes.TrimRight(line, "\r"))
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	w.sink.Progress(types.ProgressEvent{
		Kind:    types.ProgressLog,
		Machine: w.machine,
		Message: text,
	})
}
