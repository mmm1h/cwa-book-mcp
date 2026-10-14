package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	bolt "go.etcd.io/bbolt"
)

var names = []string{"meta", "tasks", "active", "history", "exact", "idem", "idem_tasks", "candidates", "cursors", "cache_expiry", "cwa_ids"}

type Store struct{ db *bolt.DB }
type Cache struct {
	Expires time.Time       `json:"expires"`
	Data    json.RawMessage `json:"data"`
}
type Idem struct {
	Hash   string          `json:"hash"`
	TaskID string          `json:"task_id"`
	Result json.RawMessage `json:"result"`
}

func Open(dir string) (*Store, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	db, e := bolt.Open(filepath.Join(dir, "state.db"), 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 1 << 20})
	if e != nil {
		return nil, e
	}
	s := &Store{db}
	e = db.Update(func(tx *bolt.Tx) error {
		for _, n := range names {
			if _, e := tx.CreateBucketIfNotExists([]byte(n)); e != nil {
				return e
			}
		}
		m := tx.Bucket([]byte("meta"))
		v := m.Get([]byte("schema_version"))
		if v != nil && string(v) != "1" {
			return errors.New("unsupported state schema; backup and migrate before starting")
		}
		return m.Put([]byte("schema_version"), []byte("1"))
	})
	if e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Backup(w io.Writer) error {
	return s.db.View(func(tx *bolt.Tx) error { _, e := tx.WriteTo(w); return e })
}
func bucket(tx *bolt.Tx, n string) *bolt.Bucket { return tx.Bucket([]byte(n)) }
func read(tx *bolt.Tx, id string) (domain.Task, error) {
	var t domain.Task
	b := bucket(tx, "tasks").Get([]byte(id))
	if b == nil {
		return t, domain.E("TASK_NOT_FOUND", "任务不存在或历史已清理")
	}
	e := json.Unmarshal(b, &t)
	return t, e
}
func (s *Store) Get(id string) (domain.Task, error) {
	var t domain.Task
	e := s.db.View(func(tx *bolt.Tx) error { var e error; t, e = read(tx, id); return e })
	return t, e
}
func save(tx *bolt.Tx, t domain.Task) error {
	if t.CWA != nil {
		if e := rememberCWA(tx, *t.CWA, 7*24*time.Hour); e != nil {
			return e
		}
	}
	if t.State == "ready" {
		t.Candidates = nil
	}
	previous, e := read(tx, t.ID)
	if e == nil {
		bucket(tx, "active").Delete([]byte(t.ID))
		if domain.Terminal(previous.State) {
			bucket(tx, "history").Delete([]byte(timeKey(previous.Updated, t.ID)))
		}
		if previous.ExactKey != "" {
			bucket(tx, "exact").Delete([]byte(previous.ExactKey))
		}
	}
	if domain.Terminal(t.State) {
		if e := bucket(tx, "history").Put([]byte(timeKey(t.Updated, t.ID)), []byte(t.ID)); e != nil {
			return e
		}
	} else {
		if e := bucket(tx, "active").Put([]byte(t.ID), []byte(t.ID)); e != nil {
			return e
		}
		if t.ExactKey != "" {
			if e := bucket(tx, "exact").Put([]byte(t.ExactKey), []byte(t.ID)); e != nil {
				return e
			}
		}
	}
	b, e := json.Marshal(t)
	if e != nil {
		return e
	}
	return bucket(tx, "tasks").Put([]byte(t.ID), b)
}
func timeKey(t time.Time, id string) string { return fmt.Sprintf("%020d:%s", t.UnixNano(), id) }
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Store) Replay(key, hash string) (json.RawMessage, bool, error) {
	var out json.RawMessage
	found := false
	e := s.db.View(func(tx *bolt.Tx) error {
		b := bucket(tx, "idem").Get([]byte(key))
		if b == nil {
			return nil
		}
		var r Idem
		if e := json.Unmarshal(b, &r); e != nil {
			return e
		}
		if r.Hash != hash {
			return domain.E("IDEMPOTENCY_CONFLICT", "request_id 已用于不同参数")
		}
		found = true
		out = append(out, r.Result...)
		return nil
	})
	return out, found, e
}

type Mutation func(t *domain.Task) error

// Create atomically checks idempotency and exact active-source deduplication.
func (s *Store) Create(key, hash string, t domain.Task, result func(domain.Task) any) (json.RawMessage, error) {
	var out json.RawMessage
	e := s.db.Update(func(tx *bolt.Tx) error {
		if b := bucket(tx, "idem").Get([]byte(key)); b != nil {
			var r Idem
			if e := json.Unmarshal(b, &r); e != nil {
				return e
			}
			if r.Hash != hash {
				return domain.E("IDEMPOTENCY_CONFLICT", "request_id 已用于不同参数")
			}
			out = append(out, r.Result...)
			return nil
		}
		if t.ExactKey != "" {
			if id := bucket(tx, "exact").Get([]byte(t.ExactKey)); id != nil {
				old, e := read(tx, string(id))
				if e != nil {
					return e
				}
				if !domain.Terminal(old.State) && time.Now().Before(old.Expires) {
					t = old
				}
			}
		}
		if bucket(tx, "tasks").Get([]byte(t.ID)) == nil {
			if bucket(tx, "active").Stats().KeyN >= 32 {
				return domain.Temporary("BUSY", "未结束任务已达 32 个")
			}
			if e := save(tx, t); e != nil {
				return e
			}
		}
		var e error
		out, e = json.Marshal(result(t))
		if e != nil {
			return e
		}
		return putIdem(tx, key, hash, t.ID, out)
	})
	return out, e
}
func putIdem(tx *bolt.Tx, key, hash, id string, out json.RawMessage) error {
	prefix := []byte(id + "\x00")
	c := bucket(tx, "idem_tasks").Cursor()
	count := 0
	for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		count++
		if count >= 64 {
			return domain.Temporary("BUSY", "该任务的幂等操作记录已达上限")
		}
	}
	r, e := json.Marshal(Idem{hash, id, out})
	if e != nil {
		return e
	}
	if e = bucket(tx, "idem").Put([]byte(key), r); e != nil {
		return e
	}
	return bucket(tx, "idem_tasks").Put(append(prefix, []byte(key)...), []byte(key))
}
func (s *Store) Act(key, hash, id string, mutate Mutation, result func(domain.Task) any) (json.RawMessage, error) {
	var out json.RawMessage
	e := s.db.Update(func(tx *bolt.Tx) error {
		if b := bucket(tx, "idem").Get([]byte(key)); b != nil {
			var r Idem
			if e := json.Unmarshal(b, &r); e != nil {
				return e
			}
			if r.Hash != hash {
				return domain.E("IDEMPOTENCY_CONFLICT", "request_id 已用于不同参数")
			}
			out = append(out, r.Result...)
			return nil
		}
		t, e := read(tx, id)
		if e != nil {
			return e
		}
		if e = mutate(&t); e != nil {
			return e
		}
		t.Updated = time.Now().UTC()
		if e = save(tx, t); e != nil {
			return e
		}
		out, e = json.Marshal(result(t))
		if e != nil {
			return e
		}
		return putIdem(tx, key, hash, id, out)
	})
	return out, e
}

// Change performs a short, serialized state transition. It never runs I/O.
func (s *Store) Change(id string, f Mutation) (domain.Task, error) {
	var t domain.Task
	e := s.db.Update(func(tx *bolt.Tx) error {
		var e error
		t, e = read(tx, id)
		if e != nil {
			return e
		}
		if e = f(&t); e != nil {
			return e
		}
		t.Updated = time.Now().UTC()
		return save(tx, t)
	})
	return t, e
}
func (s *Store) Active() ([]domain.Task, error) {
	out := make([]domain.Task, 0, 32)
	e := s.db.View(func(tx *bolt.Tx) error {
		return bucket(tx, "active").ForEach(func(k, v []byte) error {
			if len(out) >= 32 {
				return errors.New("active task limit exceeded")
			}
			t, e := read(tx, string(k))
			if e != nil {
				return e
			}
			out = append(out, t)
			return nil
		})
	})
	return out, e
}
func (s *Store) PutCache(kind, id string, v any, ttl time.Duration) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if b, ok := v.(domain.Book); ok && b.Origin == "cwa" {
			if e := rememberCWA(tx, b, ttl); e != nil {
				return e
			}
		}
		max := 1000
		if kind == "cursors" {
			max = 100
		}
		b := bucket(tx, kind)
		if b == nil {
			return errors.New("invalid cache")
		}
		if b.Get([]byte(id)) == nil && b.Stats().KeyN >= max {
			// Evict the oldest matching expiry record, rather than growing the cache.
			c := bucket(tx, "cache_expiry").Cursor()
			for k, value := c.First(); k != nil; k, value = c.Next() {
				var ref struct{ Kind, ID string }
				if json.Unmarshal(value, &ref) == nil && ref.Kind == kind {
					b.Delete([]byte(ref.ID))
					c.Delete()
					break
				}
			}
		}
		data, e := json.Marshal(v)
		if e != nil {
			return e
		}
		if len(data) > 128<<10 {
			return domain.E("METADATA_TOO_LARGE", "缓存上下文超过上限")
		}
		expires := time.Now().UTC().Add(ttl)
		rec, e := json.Marshal(Cache{expires, data})
		if e != nil {
			return e
		}
		if old := b.Get([]byte(id)); old != nil {
			var r Cache
			_ = json.Unmarshal(old, &r)
			bucket(tx, "cache_expiry").Delete([]byte(timeKey(r.Expires, kind+":"+id)))
		}
		if e = b.Put([]byte(id), rec); e != nil {
			return e
		}
		ref, _ := json.Marshal(struct{ Kind, ID string }{kind, id})
		return bucket(tx, "cache_expiry").Put([]byte(timeKey(expires, kind+":"+id)), ref)
	})
}
func (s *Store) GetCache(kind, id string, v any) error {
	return s.db.View(func(tx *bolt.Tx) error {
		b := bucket(tx, kind).Get([]byte(id))
		code := "CANDIDATE_EXPIRED"
		if kind == "cursors" {
			code = "CURSOR_INVALID"
		}
		if b == nil {
			return domain.E(code, "候选或游标已失效，请重新搜索")
		}
		var r Cache
		if e := json.Unmarshal(b, &r); e != nil {
			return e
		}
		if time.Now().After(r.Expires) {
			return domain.E(code, "候选或游标已过期，请重新搜索")
		}
		return json.Unmarshal(r.Data, v)
	})
}
func (s *Store) KnownCWA(id string) (domain.Book, error) {
	var b domain.Book
	e := s.db.View(func(tx *bolt.Tx) error {
		raw := bucket(tx, "cwa_ids").Get([]byte(id))
		if raw == nil {
			return domain.E("INVALID_ARGUMENT", "请使用近期 CWA 搜索结果或 ready 任务的 ID")
		}
		var rec Cache
		if e := json.Unmarshal(raw, &rec); e != nil {
			return e
		}
		if time.Now().After(rec.Expires) {
			return domain.E("INVALID_ARGUMENT", "CWA ID 的缓存已过期，请重新搜索或提供 task_id")
		}
		return json.Unmarshal(rec.Data, &b)
	})
	return b, e
}
func rememberCWA(tx *bolt.Tx, b domain.Book, ttl time.Duration) error {
	data, e := json.Marshal(b)
	if e != nil {
		return e
	}
	records := bucket(tx, "cwa_ids")
	old := records.Get([]byte(b.ID))
	expires := time.Now().Add(ttl)
	if old != nil {
		var previous Cache
		_ = json.Unmarshal(old, &previous)
		if previous.Expires.After(expires) {
			expires = previous.Expires
		}
	} else if records.Stats().KeyN >= 1000 {
		k, _ := records.Cursor().First()
		if k != nil {
			if e := records.Delete(k); e != nil {
				return e
			}
		}
	}
	raw, e := json.Marshal(Cache{expires, data})
	if e != nil {
		return e
	}
	return records.Put([]byte(b.ID), raw)
}

// RecentCWA is deliberately narrower than the download lookup: resolve accepts
// only this task's displayed candidates or a recent search, not arbitrary IDs.
func (s *Store) RecentCWA(id string) (domain.Book, error) {
	var out domain.Book
	found := false
	e := s.db.View(func(tx *bolt.Tx) error {
		if e := bucket(tx, "candidates").ForEach(func(k, v []byte) error {
			var c Cache
			var b domain.Book
			if json.Unmarshal(v, &c) == nil && time.Now().Before(c.Expires) && json.Unmarshal(c.Data, &b) == nil && b.Origin == "cwa" && b.ID == id {
				out = b
				found = true
			}
			return nil
		}); e != nil {
			return e
		}
		return nil
	})
	if e != nil {
		return out, e
	}
	if !found {
		return out, domain.E("INVALID_ARGUMENT", "请使用近期 CWA 搜索结果或已验证任务中的 ID")
	}
	return out, nil
}

// Sweep uses time indexes and caps each maintenance transaction at 128 records.
func (s *Store) Sweep(now time.Time) ([]string, error) {
	var ids []string
	e := s.db.Update(func(tx *bolt.Tx) error {
		c := bucket(tx, "cache_expiry").Cursor()
		for n := 0; n < 128; n++ {
			k, v := c.First()
			if k == nil || string(k) > timeKey(now, "") {
				break
			}
			var ref struct{ Kind, ID string }
			if json.Unmarshal(v, &ref) == nil {
				bucket(tx, ref.Kind).Delete([]byte(ref.ID))
			}
			if e := c.Delete(); e != nil {
				return e
			}
		}
		h := bucket(tx, "history")
		for n := 0; n < 128; n++ {
			k, v := h.Cursor().First()
			if k == nil {
				break
			}
			t, e := read(tx, string(v))
			if e != nil {
				return e
			}
			if h.Stats().KeyN <= 1000 && now.Before(t.Updated.Add(7*24*time.Hour)) {
				break
			}
			id := t.ID
			ids = append(ids, id)
			if e = bucket(tx, "tasks").Delete([]byte(id)); e != nil {
				return e
			}
			if e = h.Delete(k); e != nil {
				return e
			}
			prefix := []byte(id + "\x00")
			ic := bucket(tx, "idem_tasks").Cursor()
			for ik, iv := ic.Seek(prefix); ik != nil && bytes.HasPrefix(ik, prefix); ik, iv = ic.Next() {
				if e = bucket(tx, "idem").Delete(iv); e != nil {
					return e
				}
				if e = ic.Delete(); e != nil {
					return e
				}
			}
		}
		return nil
	})
	return ids, e
}
