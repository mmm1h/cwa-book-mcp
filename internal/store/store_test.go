package store

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/domain"
)

func testTask() domain.Task {
	now := time.Now().UTC()
	return domain.Task{ID: domain.ID(), State: "queued", Created: now, Updated: now, Expires: now.Add(time.Hour)}
}
func TestConcurrentIdempotencyAndRecovery(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	task := testTask()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.Create("request", "same", task, func(t domain.Task) any { return t.ID })
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if _, e = s.Create("request", "different", task, func(t domain.Task) any { return t.ID }); domain.AsError(e).Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(e)
	}
	active, e := s.Active()
	if e != nil || len(active) != 1 {
		t.Fatal(active, e)
	}
	s.Close()
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	out, ok, e := s.Replay("request", "same")
	var id string
	json.Unmarshal(out, &id)
	if e != nil || !ok || id != task.ID {
		t.Fatal(string(out), ok, e)
	}
}
func TestActiveDedupAndLimits(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	first := testTask()
	first.ExactKey = "source-key"
	s.Create("r1", "h1", first, func(t domain.Task) any { return t.ID })
	other := testTask()
	other.ExactKey = "source-key"
	out, e := s.Create("r2", "h2", other, func(t domain.Task) any { return t.ID })
	var id string
	json.Unmarshal(out, &id)
	if e != nil || id != first.ID {
		t.Fatal(e, id)
	}
	for i := 0; i < 31; i++ {
		v := testTask()
		if _, e = s.Create(fmt.Sprint(i), "h", v, func(t domain.Task) any { return t.ID }); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.Create("overflow", "h", testTask(), func(t domain.Task) any { return t.ID }); domain.AsError(e).Code != "BUSY" {
		t.Fatal(e)
	}
	s.Change(first.ID, func(t *domain.Task) error { t.State = "cancelled"; return nil })
	if _, e = s.Create("after-cancel", "h", testTask(), func(t domain.Task) any { return t.ID }); e != nil {
		t.Fatal(e)
	}
}

func TestExpiredTaskCleanupPreservesNewSourceDedup(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := testTask()
	old.ExactKey, old.Expires = "same-source", time.Now().Add(-time.Second)
	newer := testTask()
	newer.ExactKey = old.ExactKey
	result := func(t domain.Task) any { return t.ID }
	if _, err = s.Create("old", "old", old, result); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create("new", "new", newer, result); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Change(old.ID, func(t *domain.Task) error { t.State = "cancelled"; return nil }); err != nil {
		t.Fatal(err)
	}
	third := testTask()
	third.ExactKey = old.ExactKey
	raw, err := s.Create("third", "third", third, result)
	var id string
	json.Unmarshal(raw, &id)
	if err != nil || id != newer.ID {
		t.Fatal("expired task cleanup lost deduplication", id, err)
	}
}
func TestCacheExpiryAndHistoryCleanup(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.PutCache("candidates", "expired", domain.Book{Title: "test"}, -time.Second)
	var b domain.Book
	if e = s.GetCache("candidates", "expired", &b); domain.AsError(e).Code != "CANDIDATE_EXPIRED" {
		t.Fatal(e)
	}
	task := testTask()
	task.State = "ready"
	task.Updated = time.Now().Add(-8 * 24 * time.Hour)
	s.Create("old", "h", task, func(t domain.Task) any { return t.ID })
	ids, e := s.Sweep(time.Now())
	if e != nil || len(ids) != 1 {
		t.Fatal(ids, e)
	}
	if _, ok, e := s.Replay("old", "h"); e != nil || ok {
		t.Fatal(ok, e)
	}
	if _, e = s.Get(task.ID); domain.AsError(e).Code != "TASK_NOT_FOUND" {
		t.Fatal(e)
	}
}
