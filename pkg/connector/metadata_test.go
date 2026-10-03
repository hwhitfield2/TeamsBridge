package connector

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestConcurrentMetadataSave(t *testing.T) {
	m := &Metadata{Cursors: map[string]time.Time{}}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			m.mu.Lock()
			m.Cursors["chat"] = time.Now()
			m.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			if _, err := json.Marshal(m); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
}
