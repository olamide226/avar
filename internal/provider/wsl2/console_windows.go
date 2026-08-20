//go:build windows

package wsl2

import (
	"os"
	"os/signal"
)

// holdInterrupts stops avar dying of the Ctrl-C that is meant for the guest, and
// returns a function that restores the default behaviour.
//
// Nothing is forwarded, because nothing needs to be. Windows delivers a console
// control event to every process attached to the console, so a Ctrl-C typed
// during `avr npm test` reaches wsl.exe — and through it the guest — without avar
// being in the path at all. That is the same shape as the Unix backend, where the
// terminal driver signals the whole foreground process group.
//
// What differs is what happens to avar itself. Go's default disposition for an
// interrupt is to terminate, so avar would exit at the moment the user pressed
// the key, before wsl.exe had reported what the guest did — and the shell that
// invoked avr would see avar's status rather than the guest's, which is exactly
// what PROP-3 forbids. Ignoring the interrupt for the duration of the call keeps
// avar alive to report it.
//
// The window in which a second Ctrl-C also does not reach avar is the price, and
// it is the right way round: the guest is still receiving them, and a user who
// wants avar itself gone has the console's own means of doing that.
func holdInterrupts() (stop func()) {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	return func() { signal.Stop(interrupts) }
}
