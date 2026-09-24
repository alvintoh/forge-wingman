package store

import "errors"
import "testing"

func TestGetUserReturnsTheRecord(t *testing.T) {
	s := New()
	u, err := s.getUser("u1")
	if err != nil {
		t.Fatalf("getUser returned %v, want nil", err)
	}
	if u.Name != "Ada Lovelace" {
		t.Errorf("Name = %q, want %q", u.Name, "Ada Lovelace")
	}
}

func TestGetUserRejectsAnUnknownID(t *testing.T) {
	s := New()
	if _, err := s.getUser("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("getUser error = %v, want ErrNotFound", err)
	}
}

func TestNameReadsThroughGetUser(t *testing.T) {
	s := New()
	got, err := s.Name("u2")
	if err != nil {
		t.Fatalf("Name returned %v, want nil", err)
	}
	if got != "Grace Hopper" {
		t.Errorf("Name = %q, want %q", got, "Grace Hopper")
	}
}

func TestLookupByEmailIgnoresCaseAndSpace(t *testing.T) {
	s := New()
	u, err := s.LookupByEmail("  ALAN@example.com ")
	if err != nil {
		t.Fatalf("LookupByEmail returned %v, want nil", err)
	}
	if u.ID != "u3" {
		t.Errorf("ID = %q, want %q", u.ID, "u3")
	}
}
