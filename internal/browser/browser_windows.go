package browser

import (
	"context"

	"golang.org/x/sys/windows"
)

// openURL asks the Windows shell to open address, which it does with the
// default browser registered for the scheme.
//
// ShellExecuteW takes the address as one wide string argument, so no command
// interpreter ever parses it. It returns once the request is handed over; the
// context is checked first because the call itself cannot be cancelled.
func openURL(ctx context.Context, address string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
