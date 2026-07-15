package auth

import (
	"reflect"
	"testing"
)

func TestScopeSetValidationAndNames(t *testing.T) {
	t.Parallel()

	set, err := NewScopeSet(ScopeMessageRead, ScopeInboxRead, ScopeMessageRead)
	if err != nil {
		t.Fatalf("NewScopeSet() error = %v", err)
	}
	if !set.Has(ScopeInboxRead, ScopeMessageRead) || set.Has(ScopeInboxDelete) {
		t.Fatalf("ScopeSet.Has() unexpected for mask %d", set.Mask())
	}
	if set.Has(0) || set.Has(ScopeInboxRead|ScopeMessageRead) || set.Has(1<<20) {
		t.Fatalf("ScopeSet.Has() accepted an invalid required scope for mask %d", set.Mask())
	}
	wantNames := []string{"inbox:read", "message:read"}
	if got := set.Names(); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("ScopeSet.Names() = %v, want %v", got, wantNames)
	}
	restored, err := ScopeSetFromMask(set.Mask())
	if err != nil || restored != set {
		t.Fatalf("ScopeSetFromMask() = %d, %v", restored, err)
	}
}

func TestScopeSetRejectsEmptyCompositeAndUnknownScopes(t *testing.T) {
	t.Parallel()

	if _, err := NewScopeSet(); err == nil {
		t.Fatal("NewScopeSet(empty) error = nil")
	}
	if _, err := NewScopeSet(ScopeInboxRead | ScopeMessageRead); err == nil {
		t.Fatal("NewScopeSet(composite) error = nil")
	}
	if _, err := NewScopeSet(1 << 20); err == nil {
		t.Fatal("NewScopeSet(unknown) error = nil")
	}
	if _, err := ScopeSetFromMask(0); err == nil {
		t.Fatal("ScopeSetFromMask(empty) error = nil")
	}
	if _, err := ScopeSetFromMask(1 << 20); err == nil {
		t.Fatal("ScopeSetFromMask(unknown) error = nil")
	}
}
