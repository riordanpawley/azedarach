package sqlitemigration_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/daemon/notices"
	operationstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	_ "modernc.org/sqlite"
)

func TestProjectMigrationAuthoritiesOpenSharedDatabaseInEveryOrder(t *testing.T) {
	for _, order := range authorityOrders([]string{"issues", "operations", "notices"}) {
		name := fmt.Sprintf("%s-%s-%s", order[0], order[1], order[2])
		t.Run(name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "azedarach.db")
			for _, authority := range order {
				if err := openProjectAuthority(dbPath, authority); err != nil {
					t.Fatalf("open %s: %v", authority, err)
				}
			}
			for _, authority := range []string{"issues", "operations", "notices"} {
				if err := openProjectAuthority(dbPath, authority); err != nil {
					t.Fatalf("reopen %s: %v", authority, err)
				}
			}

			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rows, err := db.Query(`
				SELECT authority_id, format_version, length(artifact_checksum)
				FROM schema_migration_checksum_conversions
				ORDER BY authority_id
			`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			want := []string{"project.daemon_notices", "project.daemon_operations", "project.issues"}
			var got []string
			for rows.Next() {
				var authority string
				var version, checksumLength int
				if err := rows.Scan(&authority, &version, &checksumLength); err != nil {
					t.Fatal(err)
				}
				if version != 2 || checksumLength != 64 {
					t.Fatalf("marker %s version/checksum length = %d/%d", authority, version, checksumLength)
				}
				got = append(got, authority)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("authorities = %v, want %v", got, want)
			}
			var blankKnown int
			if err := db.QueryRow(`
				SELECT count(*) FROM schema_migrations
				WHERE (artifact_checksum IS NULL OR trim(artifact_checksum)='')
				  AND (id LIKE 'daemon_operations_%' OR id LIKE 'daemon_notices_%')
			`).Scan(&blankKnown); err != nil {
				t.Fatal(err)
			}
			if blankKnown != 0 {
				t.Fatalf("blank known daemon migration checksums = %d", blankKnown)
			}
		})
	}
}

func openProjectAuthority(dbPath, authority string) error {
	ctx := context.Background()
	switch authority {
	case "issues":
		store := issues.NewClientAtPath(dbPath, nil)
		if _, err := store.List(ctx); err != nil {
			return err
		}
		return store.CloseDB()
	case "operations":
		store := operationstore.NewAtPath(dbPath, nil)
		if _, err := store.List(ctx, operationstore.Query{Limit: 1}); err != nil {
			return err
		}
		return store.Close()
	case "notices":
		store := notices.NewAtPath(dbPath, nil)
		if _, err := store.List(ctx, notices.Query{Limit: 1}); err != nil {
			return err
		}
		return store.Close()
	default:
		return fmt.Errorf("unknown authority %q", authority)
	}
}

func authorityOrders(values []string) [][]string {
	if len(values) == 1 {
		return [][]string{{values[0]}}
	}
	var result [][]string
	for index, value := range values {
		rest := append(append([]string(nil), values[:index]...), values[index+1:]...)
		for _, suffix := range authorityOrders(rest) {
			result = append(result, append([]string{value}, suffix...))
		}
	}
	return result
}
