package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type WatchOptions struct {
	Debounce time.Duration
	OnChange func()
}

type Watcher struct {
	debounce time.Duration
	onChange func()

	mu         sync.Mutex
	roots      map[string]struct{}
	suppressed map[string]time.Time
	lastScan   map[string]string
	timer      *time.Timer
	closed     bool
}

func NewWatcher(opts WatchOptions) (*Watcher, error) {
	debounce := opts.Debounce
	if debounce <= 0 {
		debounce = 250 * time.Millisecond
	}
	return &Watcher{
		debounce:   debounce,
		onChange:   opts.OnChange,
		roots:      make(map[string]struct{}),
		suppressed: make(map[string]time.Time),
		lastScan:   make(map[string]string),
	}, nil
}

func (w *Watcher) AddRoot(root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.roots[filepath.Clean(abs)] = struct{}{}
	w.lastScan = w.snapshotLocked()
	return nil
}

func (w *Watcher) SuppressPath(path string, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.suppressed[filepath.Clean(abs)] = time.Now().Add(ttl)
}

func (w *Watcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	w.mu.Lock()
	if len(w.lastScan) == 0 {
		w.lastScan = w.snapshotLocked()
	}
	w.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			w.scan()
		}
	}
}

func (w *Watcher) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.timer != nil {
		w.timer.Stop()
	}
	return nil
}

func (w *Watcher) scan() {
	w.mu.Lock()
	current := w.snapshotLocked()
	if mapsEqual(w.lastScan, current) {
		w.mu.Unlock()
		return
	}
	changedPaths := diffPaths(w.lastScan, current)
	onlySuppressed := len(changedPaths) > 0
	now := time.Now()
	for path, until := range w.suppressed {
		if now.After(until) {
			delete(w.suppressed, path)
		}
	}
	for _, changed := range changedPaths {
		suppressed := false
		for prefix, until := range w.suppressed {
			if now.Before(until) && withinPrefix(changed, prefix) {
				suppressed = true
				break
			}
		}
		if !suppressed {
			onlySuppressed = false
			break
		}
	}
	w.lastScan = current
	if !onlySuppressed {
		w.scheduleLocked()
	}
	w.mu.Unlock()
}

func (w *Watcher) scheduleLocked() {
	if w.onChange == nil || w.closed {
		return
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.debounce, w.onChange)
}

func (w *Watcher) snapshotLocked() map[string]string {
	roots := make([]string, 0, len(w.roots))
	for root := range w.roots {
		roots = append(roots, root)
	}
	sort.Strings(roots)

	snapshot := make(map[string]string)
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			value := info.ModTime().UTC().Format(time.RFC3339Nano) + "|" + filepath.Base(path)
			if info.Mode().IsRegular() {
				if data, err := os.ReadFile(path); err == nil {
					sum := sha256.Sum256(data)
					value += "|" + hex.EncodeToString(sum[:])
				}
			}
			snapshot[path] = value
			return nil
		})
	}
	return snapshot
}

func diffPaths(prev, next map[string]string) []string {
	seen := make(map[string]struct{}, len(prev)+len(next))
	var changed []string
	for path, val := range prev {
		seen[path] = struct{}{}
		if next[path] != val {
			changed = append(changed, path)
		}
	}
	for path, val := range next {
		if _, ok := seen[path]; ok {
			continue
		}
		if prev[path] != val {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, val := range a {
		if b[key] != val {
			return false
		}
	}
	return true
}

func withinPrefix(path, prefix string) bool {
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+string(filepath.Separator))
}
