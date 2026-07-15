package duckmail

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	encoded, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	if !verifyPassword(encoded, "correct horse battery staple") {
		t.Fatal("verifyPassword(correct) = false")
	}
	if verifyPassword(encoded, "wrong password") {
		t.Fatal("verifyPassword(wrong) = true")
	}
	if verifyPassword("invalid", "correct horse battery staple") {
		t.Fatal("verifyPassword(invalid hash) = true")
	}
}
