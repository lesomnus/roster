package core

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/server/portrait"
)

// TestACallThatEndedWaitingToBeRenderedIsNotAnImageRefused: an image no
// portrait was made of has two reasons, and they are two answers. One roster
// will not keep is `InvalidArgument`, which a front door reads as *keep the
// URL alone and do not send this again* (`arrives/fill.go`). A call that ended
// while it waited its turn -- a process renders a few images at once
// ([portrait.AtOnce]) -- said nothing wrong, and answered the same way it
// would have had its picture dropped for being asked for at a busy moment.
func TestACallThatEndedWaitingToBeRenderedIsNotAnImageRefused(t *testing.T) {
	x := require.New(t)

	of := func(w, h int) []byte {
		var b bytes.Buffer
		x.NoError(png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))))

		return b.Bytes()
	}
	rendered := func(ctx context.Context, b []byte) codes.Code {
		_, err := portrait.Render(ctx, b)
		x.Error(err)

		return status.Code(unrendered(err))
	}

	x.Equal(codes.InvalidArgument, rendered(t.Context(), []byte("<svg/>")))
	x.Equal(codes.InvalidArgument, rendered(t.Context(), of(portrait.MaxSide+1, 1)))

	// A call that is over is the one case a wait ends in without anybody
	// holding a turn: it is not given one.
	late, cancel := context.WithTimeout(t.Context(), 0)
	defer cancel()
	x.Equal(codes.DeadlineExceeded, rendered(late, of(64, 64)), "a call that ran out of time waiting is answered as an image refused")

	gone, cancel := context.WithCancel(t.Context())
	cancel()
	x.Equal(codes.Canceled, rendered(gone, of(64, 64)))
}
