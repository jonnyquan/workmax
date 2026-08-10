//go:build desktop

package cloud_proxy

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// SSEIdleTimeout is how long an SSE upstream may go without delivering a
// single byte before the relay gives up on the connection. Model turns can
// legitimately stall for a while between tokens, and healthy upstreams emit
// keepalive comments well inside this window; a connection silent for two
// minutes is a half-open socket, not a slow answer.
const SSEIdleTimeout = 120 * time.Second

// ErrSSEUpstreamIdle marks a turn that was cut because the upstream stopped
// sending data entirely. It is classified retryable: nothing about the request
// was wrong, the connection under it died.
var ErrSSEUpstreamIdle = errors.New("sse: upstream sent no data before idle timeout")

// IdleWatchdogReader wraps an SSE response body with an idle watchdog.
//
// The chat HTTP clients deliberately run with Timeout: 0 (a turn lives for
// minutes), and there is no per-read deadline anywhere under them — so a
// half-open upstream connection used to park the turn forever: the scanner
// blocked in Read, the renderer saw only our own keepalive comments, and
// nothing ever failed. The watchdog is the missing bound: a goroutine arms a
// timer that every delivered byte resets, and if the timer fires it calls
// interrupt (typically resp.Body.Close), which forces the pending Read to
// return an error and unblocks the pipe. TimedOut then lets the caller
// reclassify that read error as the retryable idle interruption it really is.
type IdleWatchdogReader struct {
	src       io.Reader
	interrupt func()
	timeout   time.Duration

	activity chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	fired    atomic.Bool
}

// NewIdleWatchdogReader starts the watchdog. interrupt is invoked exactly once,
// from the watchdog goroutine, if timeout passes with no bytes read; it must
// unblock a Read in progress (resp.Body.Close does). Stop must be called when
// the pipe finishes so the goroutine and timer are released.
func NewIdleWatchdogReader(src io.Reader, timeout time.Duration, interrupt func()) *IdleWatchdogReader {
	r := &IdleWatchdogReader{
		src:       src,
		interrupt: interrupt,
		timeout:   timeout,
		activity:  make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	go r.watch()
	return r
}

func (r *IdleWatchdogReader) watch() {
	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-r.activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(r.timeout)
		case <-timer.C:
			r.fired.Store(true)
			r.interrupt()
			return
		}
	}
}

// Read delegates to the wrapped body and feeds the watchdog. Any delivered
// bytes count as life — including upstream keepalive comments, which is
// correct: the connection is demonstrably alive, the model is just thinking.
func (r *IdleWatchdogReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	if n > 0 {
		select {
		case r.activity <- struct{}{}:
		default:
		}
	}
	return n, err
}

// Stop retires the watchdog. Idempotent; safe to defer alongside the body's
// own Close. After Stop the timer can no longer fire, but a fire that already
// happened stays visible through TimedOut.
func (r *IdleWatchdogReader) Stop() {
	r.stopOnce.Do(func() { close(r.done) })
}

// TimedOut reports whether the watchdog fired. When it did, the read error the
// pipe surfaced is our own doing (body closed under the reader) and should be
// reported as ErrSSEUpstreamIdle rather than a generic stream failure.
func (r *IdleWatchdogReader) TimedOut() bool { return r.fired.Load() }
