package portrait

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAFewPicturesAreRenderedAtOnceAndTheRestWait: nothing bounded how many
// images a process rendered at once, each of them the memory of an image
// decoded whole, and `HolderService/Portray` is everybody's to call. Eight of
// the largest at once made a heap of two gibibytes.
//
// At most [AtOnce] are rendered at once. Held by taking every turn, as that
// many renders in flight do: the next waits, and gives up with why its call
// ended and not with anything about the image; one that needs no rendering --
// too large, or no image -- is refused at once and waits for nothing; and a
// turn given back is taken by whoever is waiting.
func TestAFewPicturesAreRenderedAtOnceAndTheRestWait(t *testing.T) {
	x := require.New(t)

	of := func(w, h int) []byte {
		var b bytes.Buffer
		x.NoError(png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))))

		return b.Bytes()
	}
	picture, vast := of(64, 64), of(MaxSide+1, 1)

	for range AtOnce {
		turns <- struct{}{}
	}
	taken := true
	give := func() {
		if taken {
			for range AtOnce {
				<-turns
			}
		}
		taken = false
	}
	t.Cleanup(give)

	t.Run("one more waits, for as long as its call lasts", func(t *testing.T) {
		x := require.New(t)
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		at := time.Now()
		_, err := Render(ctx, picture)
		x.ErrorIs(err, context.DeadlineExceeded, "a render ran with every turn taken")
		x.NotErrorIs(err, ErrNotAnImage)
		x.NotErrorIs(err, ErrTooLarge, "a call that ran out of time waiting is answered as an image refused")
		x.GreaterOrEqual(time.Since(at), 50*time.Millisecond, "it did not wait")
	})

	t.Run("and one whose caller went away stops waiting", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := Render(ctx, picture)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("what is refused from its header waits for nothing", func(t *testing.T) {
		x := require.New(t)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()

		_, err := Render(ctx, vast)
		x.ErrorIs(err, ErrTooLarge)
		_, err = Render(ctx, []byte("<svg/>"))
		x.ErrorIs(err, ErrNotAnImage)
	})

	t.Run("a turn given back is taken by whoever waits", func(t *testing.T) {
		x := require.New(t)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		done := make(chan error, 1)
		go func() {
			_, err := Render(ctx, picture)
			done <- err
		}()
		select {
		case err := <-done:
			x.Failf("a render did not wait for a turn", "%v", err)
		case <-time.After(50 * time.Millisecond):
		}

		<-turns
		x.NoError(<-done)

		// And it gave back the one it took: every turn but the one freed is
		// still held, by this test.
		x.Len(turns, AtOnce-1, "a render kept its turn")
		turns <- struct{}{}
	})

	give()
	_, err := Render(t.Context(), picture)
	x.NoError(err, "with every turn free again")
	x.Empty(turns)
}
