package crons

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

const manifestFilename = ".manifest.json"

type manifestEntry struct {
	Checksum  string `json:"checksum"`
	SyncedAt  string `json:"syncedAt"`
	LastRunAt string `json:"lastRunAt,omitempty"`
}

type manifest map[string]manifestEntry

// LocalCron is one cron job materialized on disk with sync/runtime metadata.
type LocalCron struct {
	Spec            protocol.CronSpec
	SyncedAt        time.Time
	LastRunAt       *time.Time
	LocallyModified bool
}

// Store persists synced cron configs under ~/.gsd-cloud/crons/.
type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".gsd-cloud", "crons"), nil
}

func (s *Store) Dir() string {
	return s.dir
}

// Sync writes the latest server-owned cron definitions and removes stale files.
func (s *Store) Sync(jobs []protocol.CronSpec, syncedAt time.Time) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("mkdir crons dir: %w", err)
	}

	state, err := s.loadManifest()
	if err != nil {
		return err
	}

	keep := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		keep[job.ID] = struct{}{}
		data, err := json.MarshalIndent(job, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal cron %s: %w", job.ID, err)
		}
		if err := os.WriteFile(s.filePath(job.ID), data, 0o600); err != nil {
			return fmt.Errorf("write cron %s: %w", job.ID, err)
		}

		entry := state[job.ID]
		entry.Checksum = checksum(data)
		entry.SyncedAt = syncedAt.UTC().Format(time.RFC3339Nano)
		state[job.ID] = entry
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("read crons dir: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == manifestFilename || !strings.HasSuffix(name, ".json") {
			continue
		}
		jobID := strings.TrimSuffix(name, ".json")
		if _, ok := keep[jobID]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale cron %s: %w", jobID, err)
		}
		delete(state, jobID)
	}

	return s.saveManifest(state)
}

// List returns all locally materialized cron jobs sorted by ID.
func (s *Store) List() ([]LocalCron, error) {
	state, err := s.loadManifest()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read crons dir: %w", err)
	}

	var out []LocalCron
	for _, entry := range entries {
		name := entry.Name()
		if name == manifestFilename || !strings.HasSuffix(name, ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			return nil, fmt.Errorf("read cron file %s: %w", name, err)
		}

		var spec protocol.CronSpec
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("parse cron file %s: %w", name, err)
		}

		meta := state[spec.ID]
		syncedAt, _ := time.Parse(time.RFC3339Nano, meta.SyncedAt)
		var lastRunAt *time.Time
		if meta.LastRunAt != "" {
			parsed, err := time.Parse(time.RFC3339Nano, meta.LastRunAt)
			if err == nil {
				lastRunAt = &parsed
			}
		}

		out = append(out, LocalCron{
			Spec:            spec,
			SyncedAt:        syncedAt,
			LastRunAt:       lastRunAt,
			LocallyModified: meta.Checksum != "" && meta.Checksum != checksum(data),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Spec.ID < out[j].Spec.ID
	})
	return out, nil
}

// RecordRun stores the last successfully claimed occurrence time for a cron job.
func (s *Store) RecordRun(jobID string, scheduledFor time.Time) error {
	state, err := s.loadManifest()
	if err != nil {
		return err
	}
	entry := state[jobID]
	entry.LastRunAt = scheduledFor.UTC().Format(time.RFC3339Nano)
	state[jobID] = entry
	return s.saveManifest(state)
}

func (s *Store) loadManifest() (manifest, error) {
	path := filepath.Join(s.dir, manifestFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return manifest{}, nil
		}
		return nil, fmt.Errorf("read cron manifest: %w", err)
	}

	var state manifest
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse cron manifest: %w", err)
	}
	if state == nil {
		state = manifest{}
	}
	return state, nil
}

func (s *Store) saveManifest(state manifest) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("mkdir crons dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cron manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, manifestFilename), data, 0o600); err != nil {
		return fmt.Errorf("write cron manifest: %w", err)
	}
	return nil
}

func (s *Store) filePath(jobID string) string {
	return filepath.Join(s.dir, jobID+".json")
}

func checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
