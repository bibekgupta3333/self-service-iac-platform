package catalog

import (
	"context"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

func TestPostgresRepository(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	repo := NewPostgresRepository(pool, audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{}))
	svc := NewService(repo, platform.SystemClock{}, "https://example.test/iacp.git")

	for _, ver := range []string{"1.2.0", "1.10.0", "1.3.0"} {
		cmd := validIndexCommand()
		cmd.Version, cmd.GitTag = ver, "modules/sqs/v"+ver
		if _, created, err := svc.Index(ctx, bot, cmd); err != nil || !created {
			t.Fatalf("index %s: created=%v err=%v", ver, created, err)
		}
	}
	other := validIndexCommand()
	other.Module, other.Version, other.GitTag, other.Category = "s3", "0.1.0", "modules/s3/v0.1.0", "storage"
	if _, _, err := svc.Index(ctx, bot, other); err != nil {
		t.Fatalf("index s3: %v", err)
	}

	t.Run("idempotent re-index", func(t *testing.T) {
		if _, created, err := svc.Index(ctx, bot, validIndexCommand()); err != nil || created {
			t.Fatalf("created=%v err=%v", created, err)
		}
	})
	t.Run("immutable versions", func(t *testing.T) {
		cmd := validIndexCommand()
		cmd.CommitSHA = strings.Repeat("ef", 20)
		if _, _, err := svc.Index(ctx, bot, cmd); !apperr.IsKind(err, apperr.KindConflict) {
			t.Fatalf("want conflict, got %v", err)
		}
	})
	t.Run("list and filter", func(t *testing.T) {
		mods, err := svc.ListModules(ctx, ModuleFilter{})
		if err != nil || len(mods) != 2 || mods[0].Module.Name != "s3" || mods[1].Latest.Version.String() != "1.10.0" {
			t.Fatalf("ListModules = %+v, %v", mods, err)
		}
		mods, err = svc.ListModules(ctx, ModuleFilter{Category: "storage"})
		if err != nil || len(mods) != 1 || mods[0].Module.Name != "s3" {
			t.Fatalf("filtered = %+v, %v", mods, err)
		}
	})
	t.Run("version round trip", func(t *testing.T) {
		v, err := svc.GetVersion(ctx, "sqs", "1.3.0")
		if err != nil {
			t.Fatal(err)
		}
		if v.GitSHA != strings.Repeat("9c", 20) || len(v.Outputs) != 3 || !v.HasOutput("queue_arn") ||
			v.TFVersionRange != ">= 1.10, < 2.0" || len(v.InputsSchema) == 0 {
			t.Fatalf("round trip lost data: %+v", v)
		}
		if _, err := svc.GetVersion(ctx, "sqs", "9.9.9"); !apperr.IsKind(err, apperr.KindNotFound) {
			t.Fatalf("want not found, got %v", err)
		}
	})
	t.Run("audit chain written in the same transaction", func(t *testing.T) {
		res, err := audit.NewVerifier(pool).Verify(ctx, 1, 0)
		if err != nil || !res.Valid || res.Checked != 4 {
			t.Fatalf("audit = %+v, %v", res, err)
		}
	})
}
