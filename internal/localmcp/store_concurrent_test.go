package localmcp

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreConcurrentOperationRead(t *testing.T) {
	s := store{filepath.Join(canonicalTemp(t), "state")}
	id, err := handle()
	if err != nil {
		t.Fatal(err)
	}
	type record struct {
		Sequence int    `json:"sequence"`
		Result   string `json:"result"`
	}
	value := func(n int) record { return record{n, fmt.Sprintf("result-%d", n)} }
	if err := s.write(id, "operation", value(0), false); err != nil {
		t.Fatal(err)
	}
	start, stop := make(chan struct{}), make(chan struct{})
	errors := make(chan error, 4)
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				var got record
				if err := s.read(id, "operation", &got); err != nil {
					errors <- err
					return
				}
				if got != value(got.Sequence) {
					errors <- fmt.Errorf("partial operation: %+v", got)
					return
				}
			}
		})
	}
	defer func() {
		close(stop)
		readers.Wait()
		close(errors)
		for err := range errors {
			t.Error(err)
		}
	}()
	close(start)
	for n := 1; n <= 512; n++ {
		if err := s.write(id, "operation", value(n), false); err != nil {
			t.Fatal(err)
		}
	}
}
