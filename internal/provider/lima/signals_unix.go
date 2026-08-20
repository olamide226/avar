//go:build unix

package lima

import (
	"os"
	"os/signal"
	"syscall"
)

// forwardedSignals are the signals avar passes on to the guest.
//
// SIGINT and SIGTERM are Requirement 2.4. SIGWINCH is Requirement 3.1: it
// already reaches the transport directly, because the terminal driver signals
// the whole foreground process group, and relaying it as well costs nothing and
// keeps the guarantee from depending on avar and the transport sharing a group.
var forwardedSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH}

// relaySignals passes the signals a user can send avar on to the guest, and
// returns a function that stops doing so.
//
// The signal goes to avar's whole process group rather than to the limactl
// child, because the process actually holding the terminal and the guest
// session is ssh, one level further down: killing limactl alone would leave ssh
// attached to the terminal with the guest command still running, which is worse
// than not forwarding at all (REQ-2.4).
//
// This does mean a signal sent to avr reaches anything else sharing its process
// group — the other members of a pipeline it was started in, say. That is the
// same set the terminal driver signals when the user presses Ctrl-C, so the
// behaviour is the one a user of a shell already expects; and there is no
// narrower target available without putting the transport in a process group of
// its own, which would take it out of the terminal's foreground and break the
// interactive session this exists to serve.
//
// Nothing is relayed for the common case at all: a signal raised at the
// terminal is delivered to the foreground process group, which the transport is
// already part of. This is for a signal aimed at avr alone, as a script or a
// supervisor sends one.
func relaySignals() (stop func()) {
	signals := make(chan os.Signal, len(forwardedSignals))
	signal.Notify(signals, forwardedSignals...)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case sig := <-signals:
				broadcast(signals, sig)
			}
		}
	}()

	return func() {
		signal.Stop(signals)
		close(done)
	}
}

// broadcast delivers sig to every process in avar's process group.
//
// avar's own disposition is set to ignore for the duration, because it is a
// member of that group and must survive to report the guest's exit status. The
// window in which a second signal would be ignored rather than relayed is the
// price of that, and it is preferable to the alternative — a relay that kills
// the process doing the relaying.
func broadcast(signals chan<- os.Signal, sig os.Signal) {
	number, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	signal.Ignore(sig)
	// A pid of 0 means every process in the caller's own process group.
	_ = syscall.Kill(0, number)
	signal.Notify(signals, sig)
}
