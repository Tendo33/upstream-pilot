package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/config"
	"github.com/Tendo33/upstream-pilot/internal/database"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLoadSourceInventoryRequiresDatabase(t *testing.T) {
	_, _, err := (&App{}).loadSourceInventory(context.Background())
	apiErr, ok := err.(*apiError)
	if !ok || apiErr.Code != "SOURCE_DATABASE_REQUIRED" {
		t.Fatalf("err = %#v, want SOURCE_DATABASE_REQUIRED", err)
	}
}

func TestAccountFromSourceRowDoesNotRequireCredentials(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(24 * time.Hour)
	rate := 1.25
	priority := 10
	account, err := accountFromSourceRow(sourceAccountRow{
		ID:                 7,
		Name:               "primary",
		Platform:           "openai",
		Type:               "apikey",
		Status:             "active",
		Schedulable:        true,
		Priority:           20,
		Concurrency:        8,
		RateMultiplier:     &rate,
		UpdatedAt:          &now,
		Extra:              []byte(`{"quota_limit":10,"quota_used":1}`),
		ExpiresAt:          &expires,
		AutoPauseOnExpired: true,
	}, []upstream.Sub2AccountGroup{{GroupID: 9, Priority: &priority}}, map[int64]upstream.Sub2Group{
		9: {ID: 9, Name: "prod", Platform: "openai", Status: "active", RateMultiplier: &rate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if account.ID != 7 || !account.Schedulable || account.Native.Concurrency == nil || *account.Native.Concurrency != 8 {
		t.Fatalf("account = %+v", account)
	}
	if account.ObservedSourceCredentialFingerprintKnown || account.Native.MappingKnown {
		t.Fatal("source credentials must stay unread")
	}
	if !account.Native.Known || !account.Native.GroupsKnown || len(account.Native.Groups) != 1 || account.Native.Groups[0] != 9 {
		t.Fatalf("native = %+v", account.Native)
	}
	if account.Native.ExpiresAt == nil || *account.Native.ExpiresAt != expires.Unix() {
		t.Fatalf("expires = %v", account.Native.ExpiresAt)
	}
}

func TestInventorySyncReadsSourceTables(t *testing.T) {
	dsn := os.Getenv("PILOT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PILOT_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	pilotSchema := "inv_pilot_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	sourceSchema := "inv_src_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	for _, schema := range []string{pilotSchema, sourceSchema} {
		if _, err = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		schema := schema
		t.Cleanup(func() {
			_, _ = base.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		})
	}
	pilotCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pilotCfg.ConnConfig.RuntimeParams["search_path"] = pilotSchema
	pilot, err := pgxpool.NewWithConfig(ctx, pilotCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pilot.Close)
	if err = database.Migrate(ctx, pilot); err != nil {
		t.Fatal(err)
	}
	sourceCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	sourceCfg.ConnConfig.RuntimeParams["search_path"] = sourceSchema
	source, err := pgxpool.NewWithConfig(ctx, sourceCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(source.Close)
	for _, stmt := range []string{
		`CREATE TABLE groups(id bigserial PRIMARY KEY,name text NOT NULL,platform text,status text,rate_multiplier numeric,deleted_at timestamptz)`,
		`CREATE TABLE accounts(id bigserial PRIMARY KEY,name text NOT NULL,platform text NOT NULL,type text NOT NULL,extra jsonb DEFAULT '{}',concurrency int NOT NULL DEFAULT 3,load_factor int,priority int NOT NULL DEFAULT 50,rate_multiplier numeric,status text NOT NULL DEFAULT 'active',schedulable boolean NOT NULL DEFAULT true,rate_limit_reset_at timestamptz,overload_until timestamptz,temp_unschedulable_until timestamptz,expires_at timestamptz,auto_pause_on_expired boolean NOT NULL DEFAULT true,parent_account_id bigint,updated_at timestamptz,deleted_at timestamptz)`,
		`CREATE TABLE account_groups(account_id bigint NOT NULL,group_id bigint NOT NULL,priority int NOT NULL DEFAULT 50,PRIMARY KEY(account_id,group_id))`,
		`INSERT INTO groups(id,name,platform,status,rate_multiplier) VALUES(1,'prod','openai','active',1.5)`,
		`INSERT INTO accounts(id,name,platform,type,status,schedulable,priority,concurrency,rate_multiplier,extra) VALUES(7,'primary','openai','apikey','active',true,20,8,1.0,'{"quota_limit":10,"quota_used":1}')`,
		`INSERT INTO account_groups(account_id,group_id,priority) VALUES(7,1,15)`,
	} {
		if _, err = source.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	app, err := New(config.Config{MasterKey: make([]byte, 32), LogDir: t.TempDir()}, pilot, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	app.SetSourceDatabase(source)
	owner, site := uuid.NewString(), uuid.NewString()
	if _, err = pilot.Exec(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES($1,'inv@example.test','unused','admin')`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = pilot.Exec(ctx, `INSERT INTO sites(id,owner_id,name,base_url,api_key_ciphertext) VALUES($1,$2,'src','http://127.0.0.1:9','unused')`, site, owner); err != nil {
		t.Fatal(err)
	}
	if err = app.syncSite(ctx, site, owner, owner, "manual"); err != nil {
		t.Fatal(err)
	}
	var name, status string
	var schedulable bool
	var remoteID, priority int64
	var groupPriority *int
	if err = pilot.QueryRow(ctx, `SELECT a.remote_id,a.name,a.remote_status,a.schedulable,a.priority,m.group_priority FROM upstream_accounts a JOIN account_group_memberships m ON m.account_id=a.id JOIN upstream_groups g ON g.id=m.group_id WHERE a.site_id=$1 AND g.remote_id=1`, site).Scan(&remoteID, &name, &status, &schedulable, &priority, &groupPriority); err != nil {
		t.Fatal(err)
	}
	if remoteID != 7 || name != "primary" || status != "active" || !schedulable || priority != 20 || groupPriority == nil || *groupPriority != 15 {
		t.Fatalf("synced account = %d %s %s %v %d %v", remoteID, name, status, schedulable, priority, groupPriority)
	}
	var native json.RawMessage
	if err = pilot.QueryRow(ctx, `SELECT native_constraints FROM upstream_accounts WHERE site_id=$1`, site).Scan(&native); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(native), `"groups_known":true`) {
		t.Fatalf("native constraints = %s", native)
	}
}
