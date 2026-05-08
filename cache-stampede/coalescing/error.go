package coalescing

import "errors"

// ErrCoalescingTimeout is returned when a non-lock-holder polls for a
// result too many times without the lock-holder writing it.
// This usually means the lock-holder crashed or took too long.
var ErrCoalescingTimeout = errors.New("coalescing: timed out waiting for result from lock holder")
