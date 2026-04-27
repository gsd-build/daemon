package preview

import (
	"context"
	"sync"
	"time"
)

type OpenRequest struct {
	PreviewID string
	SessionID string
	ChannelID string
	MachineID string
	Target    Target
	ExpiresAt time.Time
}

type Preview struct {
	OpenRequest
	streams map[string]context.CancelFunc
}

type Registry struct {
	mu        sync.Mutex
	byID      map[string]*Preview
	bySession map[string]string
}

func NewRegistry() *Registry {
	return &Registry{
		byID:      map[string]*Preview{},
		bySession: map[string]string{},
	}
}

func (r *Registry) Open(_ context.Context, req OpenRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if oldID, ok := r.bySession[req.SessionID]; ok {
		r.closeLocked(oldID)
	}
	r.byID[req.PreviewID] = &Preview{OpenRequest: req, streams: map[string]context.CancelFunc{}}
	r.bySession[req.SessionID] = req.PreviewID
	return nil
}

func (r *Registry) Get(previewID string) (Preview, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byID[previewID]
	if !ok || time.Now().After(p.ExpiresAt) {
		if ok {
			r.closeLocked(previewID)
		}
		return Preview{}, false
	}
	return Preview{OpenRequest: p.OpenRequest}, true
}

func (r *Registry) Close(previewID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeLocked(previewID)
}

func (r *Registry) RegisterStream(previewID, streamID string) (context.Context, context.CancelFunc, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byID[previewID]
	if !ok {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.streams[streamID] = cancel
	return ctx, cancel, true
}

func (r *Registry) UnregisterStream(previewID, streamID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.byID[previewID]; ok {
		delete(p.streams, streamID)
	}
}

func (r *Registry) CancelStream(streamID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.byID {
		if cancel, ok := p.streams[streamID]; ok {
			cancel()
			delete(p.streams, streamID)
			return
		}
	}
}

func (r *Registry) closeLocked(previewID string) {
	p, ok := r.byID[previewID]
	if !ok {
		return
	}
	for _, cancel := range p.streams {
		cancel()
	}
	delete(r.bySession, p.SessionID)
	delete(r.byID, previewID)
}
