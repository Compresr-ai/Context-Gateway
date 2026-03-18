// Field reference storage for field-level compression.
// Uses formats.FieldRef as the canonical type definition.
package store

import (
	"container/list"
	"time"

	"github.com/compresr/context-gateway/internal/formats"
)

// fieldRefEntry wraps FieldRef with expiration.
type fieldRefEntry struct {
	ref       *formats.FieldRef
	expiresAt time.Time
	element   *list.Element // pointer into order list for O(1) MoveToBack/Remove
}

// SetFieldRef stores a field reference for expansion.
func (s *MemoryStore) SetFieldRef(ref *formats.FieldRef) error {
	if ref == nil || ref.ID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}

	// Initialize map if needed
	if s.fieldRefs == nil {
		s.fieldRefs = make(map[string]fieldRefEntry)
	}

	// If key exists: refresh TTL and move to back — no new list node needed.
	if existing, ok := s.fieldRefs[ref.ID]; ok {
		s.fieldRefOrder.MoveToBack(existing.element)
		s.fieldRefs[ref.ID] = fieldRefEntry{ref: ref, expiresAt: time.Now().Add(s.originalTTL), element: existing.element}
		return nil
	}

	// Cap field refs to prevent unbounded growth.
	if len(s.fieldRefs) >= s.maxFieldRefs {
		s.evictOldestFieldRef()
	}

	elem := s.fieldRefOrder.PushBack(ref.ID)
	s.fieldRefs[ref.ID] = fieldRefEntry{ref: ref, expiresAt: time.Now().Add(s.originalTTL), element: elem}
	return nil
}

// GetFieldRef retrieves a field reference by ID.
func (s *MemoryStore) GetFieldRef(refID string) (*formats.FieldRef, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.fieldRefs == nil {
		return nil, false
	}

	e, exists := s.fieldRefs[refID]
	if !exists {
		return nil, false
	}

	if time.Now().After(e.expiresAt) {
		return nil, false
	}

	return e.ref, true
}

// DeleteFieldRef removes a field reference.
func (s *MemoryStore) DeleteFieldRef(refID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped || s.fieldRefs == nil {
		return nil
	}
	if e, ok := s.fieldRefs[refID]; ok {
		s.fieldRefOrder.Remove(e.element)
		delete(s.fieldRefs, refID)
	}
	return nil
}

// SetFieldRefs stores multiple field references at once.
func (s *MemoryStore) SetFieldRefs(refs []*formats.FieldRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}

	// Initialize map if needed
	if s.fieldRefs == nil {
		s.fieldRefs = make(map[string]fieldRefEntry)
	}

	now := time.Now()
	ttl := s.originalTTL

	for _, ref := range refs {
		if ref == nil || ref.ID == "" {
			continue
		}
		// If key exists: refresh and move to back — no new list node needed.
		if existing, ok := s.fieldRefs[ref.ID]; ok {
			s.fieldRefOrder.MoveToBack(existing.element)
			s.fieldRefs[ref.ID] = fieldRefEntry{ref: ref, expiresAt: now.Add(ttl), element: existing.element}
			continue
		}
		// Enforce cap before inserting new entry.
		if len(s.fieldRefs) >= s.maxFieldRefs {
			s.evictOldestFieldRef()
		}
		elem := s.fieldRefOrder.PushBack(ref.ID)
		s.fieldRefs[ref.ID] = fieldRefEntry{ref: ref, expiresAt: now.Add(ttl), element: elem}
	}
	return nil
}
