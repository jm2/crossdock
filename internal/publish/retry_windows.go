// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package publish

import (
	"errors"
	"syscall"
)

const (
	errorAccessDenied     syscall.Errno = 5
	errorSharingViolation syscall.Errno = 32
)

// transient reports errors from replacing or removing a file that another
// process (such as crossdock serve reading current.id) has open. Go opens
// files without FILE_SHARE_DELETE, so these clear once the reader closes.
func transient(err error) bool {
	return errors.Is(err, errorSharingViolation) || errors.Is(err, errorAccessDenied)
}
