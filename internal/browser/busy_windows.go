//go:build windows

package browser

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isBusy reports a file another process holds open (Defender scanning a
// fresh file, a browser that has not released its handles yet).
func isBusy(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
