package main

import (
	"encoding/json"
	"io"
	"sync"
	"time"
	"unicode"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

// Progress is optional telemetry on stderr. Never mix it into the report stream.
func progressWriter(out io.Writer) func(audit.Progress) {
	var mu sync.Mutex
	var last time.Time
	var pending *audit.Progress
	var timer *time.Timer
	generation := 0
	phase, current := "", ""
	failed := false
	var sequence uint64
	write := func(event audit.Progress) {
		sequence++
		event.Sequence = sequence
		runes := []rune(event.Current)
		if len(runes) > 256 {
			runes = runes[:256]
		}
		for i, r := range runes {
			if unicode.IsControl(r) {
				runes[i] = ' '
			}
		}
		event.Current = string(runes)
		if json.NewEncoder(out).Encode(event) != nil {
			failed = true
		}
		phase, current, last = event.Phase, event.Current, time.Now()
	}
	return func(event audit.Progress) {
		mu.Lock()
		defer mu.Unlock()
		if failed {
			return
		}
		remaining := 50*time.Millisecond - time.Since(last)
		immediate := event.Phase != phase || event.State != "running" || (current == "" && event.Current != "")
		if immediate || remaining <= 0 {
			pending = nil
			generation++
			if timer != nil {
				timer.Stop()
				timer = nil
			}
			write(event)
			return
		}
		pending = &event
		if timer == nil {
			// Deliver the latest real count even if the next query takes a long time.
			scheduledGeneration := generation
			timer = time.AfterFunc(remaining, func() {
				mu.Lock()
				defer mu.Unlock()
				if scheduledGeneration != generation {
					return
				}
				timer = nil
				if pending != nil && !failed {
					write(*pending)
					pending = nil
				}
			})
		}
	}
}
