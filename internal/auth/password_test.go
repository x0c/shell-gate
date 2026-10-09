package authkit

import (
	"strings"
	"testing"
)

func TestPasswordHashing(t *testing.T) {
	password := strings.Repeat("test-only-", 3)
	a, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || strings.Contains(a, password) {
		t.Fatal("password was not independently salted")
	}
	if !CheckPassword(a, password) || CheckPassword(a, password+"wrong") {
		t.Fatal("incorrect password verification")
	}
	if CheckPassword(strings.Replace(a, "m=19456", "m=99999999", 1), password) {
		t.Fatal("accepted unbounded parameters")
	}
	if _, err = HashPassword("short"); err == nil {
		t.Fatal("accepted short password")
	}
}
