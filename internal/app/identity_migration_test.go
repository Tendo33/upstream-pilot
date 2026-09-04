package app

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestPilotIdentityMigrationPreservesExistingPassword(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	password := "temporary migration test password"
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	legacy := "$s2am-sha256$" + strings.TrimPrefix(hash, passwordHashPrefix)
	if _, err = a.db.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1`, w.OwnerID, legacy); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("../database/migrations/020_pilot_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(ctx, string(script)); err != nil {
		t.Fatal(err)
	}
	var updated string
	if err = a.db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, w.OwnerID).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != hash || verifyPassword(updated, password) != nil {
		t.Fatal("identity migration changed password material")
	}
	if _, err = a.db.Exec(ctx, string(script)); err != nil {
		t.Fatal(err)
	}
	var repeated string
	if err = a.db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, w.OwnerID).Scan(&repeated); err != nil {
		t.Fatal(err)
	}
	if repeated != hash {
		t.Fatal("identity migration is not idempotent")
	}
}
