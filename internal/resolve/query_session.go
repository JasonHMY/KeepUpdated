package resolve

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

type queryResult struct {
	ready chan struct{}
	data  []byte
	err   error
}

// A resolution owns one session. Fresh apply resolution gets a new session, so
// shared manager metadata never survives a new installation/source verification.
type querySession struct {
	mu      sync.Mutex
	entries map[string]*queryResult
}

func (s *querySession) run(ctx context.Context, runner manage.Runner, command manage.Command) ([]byte, error) {
	encoded, _ := json.Marshal(command)
	key := string(encoded)
	s.mu.Lock()
	if result, ok := s.entries[key]; ok {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-result.ready:
			return append([]byte(nil), result.data...), result.err
		}
	}
	result := &queryResult{ready: make(chan struct{})}
	s.entries[key] = result
	s.mu.Unlock()
	result.data, result.err = runner.Run(ctx, command)
	close(result.ready)
	return append([]byte(nil), result.data...), result.err
}
