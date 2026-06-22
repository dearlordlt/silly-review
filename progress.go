package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"silly-review/internal/review"
)

// progress renders a live "still working" indicator for headless reviews. When
// the writer is a terminal it shows a spinner with the current activity,
// updated in place; otherwise (piped/redirected) it prints one line per event.
// It always writes to the given writer (stderr in practice) so piped stdout —
// the report or --json — stays clean.
type progress struct {
	w       io.Writer
	tty     bool
	mu      sync.Mutex
	last    string
	started time.Time
	quit    chan struct{}
	wg      sync.WaitGroup
}

func newProgress(w io.Writer) *progress {
	p := &progress{w: w, quit: make(chan struct{})}
	if f, ok := w.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			p.tty = true
		}
	}
	return p
}

func (p *progress) start() {
	if !p.tty {
		return
	}
	p.started = time.Now()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		i := 0
		for {
			select {
			case <-p.quit:
				return
			case <-t.C:
				p.mu.Lock()
				msg := p.last
				p.mu.Unlock()
				el := time.Since(p.started)
				// \r returns to col 0, \033[2K clears the line.
				fmt.Fprintf(p.w, "\r\033[2K%c %s  (%s)", frames[i%len(frames)], clip(msg, 64), fmtDur(el))
				i++
			}
		}
	}()
}

// event is the review.Run callback.
func (p *progress) event(e review.Event) {
	p.mu.Lock()
	p.last = e.Text
	p.mu.Unlock()
	// Thinking deltas only feed the in-place spinner; printing them as lines
	// would flood piped output.
	if !p.tty && e.Kind != review.EvtThinking {
		fmt.Fprintf(p.w, "· %s\n", e.Text)
	}
}

func fmtDur(d time.Duration) string {
	s := int(d.Seconds())
	if s < 0 {
		s = 0
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func (p *progress) stop() {
	if !p.tty {
		return
	}
	close(p.quit)
	p.wg.Wait()
	fmt.Fprint(p.w, "\r\033[2K") // clear the spinner line
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}
