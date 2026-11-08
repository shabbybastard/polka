package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestLastAdminTriggerClosesRace(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin, err := s.CreateUser(ctx, "admin", "password", "", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET role = 'user' WHERE id = ?`, admin.ID); err == nil {
		t.Fatal("direct UPDATE demoted the last administrator")
	} else if !strings.Contains(err.Error(), "last administrator") {
		t.Errorf("unexpected trigger error: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, admin.ID); err == nil {
		t.Fatal("direct DELETE removed the last administrator")
	}
}
