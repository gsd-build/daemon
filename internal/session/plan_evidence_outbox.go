package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type planEvidenceOutbox struct {
	path string
}

func newPlanEvidenceOutbox(path string) *planEvidenceOutbox {
	if path == "" {
		return nil
	}
	return &planEvidenceOutbox{path: path}
}

func (o *planEvidenceOutbox) Append(entry planEvidencePayload) error {
	if o == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(o.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(o.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (o *planEvidenceOutbox) Load() ([]planEvidencePayload, error) {
	if o == nil {
		return nil, nil
	}
	f, err := os.Open(o.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries := []planEvidencePayload{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry planEvidencePayload
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.ID != "" {
			entries = append(entries, entry)
		}
	}
	return entries, scanner.Err()
}

func (o *planEvidenceOutbox) Ack(id string) error {
	if o == nil || id == "" {
		return nil
	}
	entries, err := o.Load()
	if err != nil {
		return err
	}
	kept := make([]planEvidencePayload, 0, len(entries))
	for _, entry := range entries {
		if entry.ID != id {
			kept = append(kept, entry)
		}
	}
	return o.replace(kept)
}

func (o *planEvidenceOutbox) replace(entries []planEvidencePayload) error {
	if o == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(o.path), 0o700); err != nil {
		return err
	}
	tmp := o.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			_ = f.Close()
			return err
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, o.path)
}
