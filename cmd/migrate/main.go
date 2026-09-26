// Command migrate applies, rolls back and generates database migrations.
//
//	migrate up                 apply all pending migrations (DATABASE_URL)
//	migrate down [n]           roll back n migrations (default 1)
//	migrate version            print the current version
//	migrate force <v>          mark version v as applied (dirty-state recovery)
//	migrate diff <name>        generate a migration from the Ent schema (DEV_DATABASE_URL, an empty scratch DB)
//	migrate hash               recompute migrations/atlas.sum after hand-editing a file
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	atlasmigrate "ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/sqltool"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"rentmapgh/internal/db"
	entmigrate "rentmapgh/internal/ent/migrate"
)

const dir = "migrations"

func main() {
	if len(os.Args) < 2 {
		fail(fmt.Errorf("usage: migrate up|down [n]|version|force <v>|diff <name>|hash"))
	}
	ctx := context.Background()
	var err error
	switch cmd := os.Args[1]; cmd {
	case "diff":
		if len(os.Args) < 3 {
			fail(fmt.Errorf("usage: migrate diff <name>"))
		}
		err = diff(ctx, os.Args[2])
	case "hash":
		err = hash()
	default:
		err = run(ctx, cmd, os.Args[2:])
	}
	if err != nil {
		fail(err)
	}
}

func run(ctx context.Context, cmd string, args []string) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	m, err := db.NewMigrator(url)
	if err != nil {
		return err
	}
	defer m.Close()
	switch cmd {
	case "up":
		return m.Up()
	case "down":
		n := 1
		if len(args) > 0 {
			if n, err = strconv.Atoi(args[0]); err != nil || n < 1 {
				return fmt.Errorf("down: n must be a positive integer")
			}
		}
		return m.Down(n)
	case "version":
		v, dirty, err := m.Version()
		if err != nil {
			return err
		}
		fmt.Printf("version=%d dirty=%t\n", v, dirty)
		return nil
	case "force":
		if len(args) < 1 {
			return fmt.Errorf("usage: migrate force <version>")
		}
		v, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("force: %w", err)
		}
		return m.Force(v)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

// diff replays the existing migrations into an empty dev database, compares the
// result with the Ent schema and writes the difference as a new migration.
// Columns/indexes not in the Ent schema (e.g. PostGIS generated columns) are
// never dropped because WithDropColumn/WithDropIndex stay off.
func diff(ctx context.Context, name string) error {
	devURL := os.Getenv("DEV_DATABASE_URL")
	if devURL == "" {
		return fmt.Errorf("DEV_DATABASE_URL is required (an empty scratch database)")
	}
	if err := hash(); err != nil {
		return err
	}
	d, err := sqltool.NewGolangMigrateDir(dir)
	if err != nil {
		return err
	}
	sqlDB, err := sql.Open("pgx", devURL)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	return entmigrate.NewSchema(entsql.OpenDB(dialect.Postgres, sqlDB)).NamedDiff(ctx, name,
		schema.WithDir(d),
		schema.WithMigrationMode(schema.ModeReplay),
		schema.WithDialect(dialect.Postgres),
		schema.WithFormatter(sqltool.GolangMigrateFormatter),
	)
}

func hash() error {
	d, err := sqltool.NewGolangMigrateDir(dir)
	if err != nil {
		return err
	}
	sum, err := d.Checksum()
	if err != nil {
		return err
	}
	return atlasmigrate.WriteSumFile(d, sum)
}

func fail(err error) {
	slog.Error("migrate", "err", err)
	os.Exit(1)
}
