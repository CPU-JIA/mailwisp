package auth

import (
	"errors"
	"sort"
)

// Scope identifies one explicitly granted capability action.
type Scope uint32

const (
	ScopeInboxRead Scope = 1 << iota
	ScopeInboxDelete
	ScopeMessageRead
	ScopeMessageUpdate
	ScopeMessageDelete
)

const allCapabilityScopes = ScopeInboxRead | ScopeInboxDelete | ScopeMessageRead | ScopeMessageUpdate | ScopeMessageDelete

// ScopeSet is a compact, canonical set of capability scopes.
type ScopeSet uint32

// NewScopeSet validates and canonicalizes capability scopes.
func NewScopeSet(scopes ...Scope) (ScopeSet, error) {
	var set ScopeSet
	for _, scope := range scopes {
		if scope == 0 || scope&allCapabilityScopes != scope || scope&(scope-1) != 0 {
			return 0, errors.New("invalid capability scope")
		}
		set |= ScopeSet(scope)
	}
	if set == 0 {
		return 0, errors.New("at least one capability scope is required")
	}
	return set, nil
}

// ScopeSetFromMask validates a persisted scope bitmask.
func ScopeSetFromMask(mask uint32) (ScopeSet, error) {
	set := ScopeSet(mask)
	if set == 0 || Scope(set)&allCapabilityScopes != Scope(set) {
		return 0, errors.New("invalid capability scope mask")
	}
	return set, nil
}

// Mask returns the stable PostgreSQL representation.
func (s ScopeSet) Mask() uint32 {
	return uint32(s)
}

// Has reports whether every required scope is present.
func (s ScopeSet) Has(required ...Scope) bool {
	for _, scope := range required {
		if scope == 0 || scope&allCapabilityScopes != scope || scope&(scope-1) != 0 || Scope(s)&scope != scope {
			return false
		}
	}
	return true
}

// Names returns stable, sorted scope names for API and audit presentation.
func (s ScopeSet) Names() []string {
	names := make([]string, 0, 5)
	for scope, name := range map[Scope]string{
		ScopeInboxRead:     "inbox:read",
		ScopeInboxDelete:   "inbox:delete",
		ScopeMessageRead:   "message:read",
		ScopeMessageUpdate: "message:update",
		ScopeMessageDelete: "message:delete",
	} {
		if s.Has(scope) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
