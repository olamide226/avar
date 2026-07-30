package types

// ProgressKind classifies a progress event so the presentation layer can
// decide how to render it without parsing message text.
type ProgressKind string

const (
	// ProgressCreating reports that a new environment is being provisioned.
	ProgressCreating ProgressKind = "creating"
	// ProgressStarting reports that an existing machine is being started.
	ProgressStarting ProgressKind = "starting"
	// ProgressMounting reports a mount registration, including the one-time
	// restart it may require.
	ProgressMounting ProgressKind = "mounting"
	// ProgressStopping reports a machine shutdown.
	ProgressStopping ProgressKind = "stopping"
	// ProgressWarning reports a condition the user should know about but
	// which does not stop the operation, such as CPU emulation.
	ProgressWarning ProgressKind = "warning"
	// ProgressLog carries raw backend output for verbose mode.
	ProgressLog ProgressKind = "log"
)

// ProgressEvent is a single unit of user-visible progress.
type ProgressEvent struct {
	Kind    ProgressKind
	Machine string
	// Message is a complete, human-readable sentence. Lower layers phrase
	// it; the presentation layer decides whether and how to show it.
	Message string
}

// ProgressSink receives progress events from long-running operations.
//
// Implementations must be safe to call from the goroutine driving the
// operation and must never block indefinitely. A nil ProgressSink is not
// valid; use DiscardProgress when output is not wanted.
type ProgressSink interface {
	Progress(ProgressEvent)
}

// ProgressFunc adapts a function to ProgressSink.
type ProgressFunc func(ProgressEvent)

// Progress implements ProgressSink.
func (f ProgressFunc) Progress(e ProgressEvent) { f(e) }

// DiscardProgress drops every event. Useful in tests and non-interactive paths.
var DiscardProgress ProgressSink = ProgressFunc(func(ProgressEvent) {})
