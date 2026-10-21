// Package neterr names the class of a transport error. The text of such an error can carry addresses from the
// request, so logs and replies carry the class and not the text.
package neterr

import (
	"context"
	"errors"
	"net"
)

func Kind(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	default:
		return "unreachable"
	}
}
