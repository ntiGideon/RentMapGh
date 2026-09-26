package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"rentmapgh/migrations"
)

// Migrator applies the embedded migrations. A Postgres advisory lock taken by
// golang-migrate makes concurrent runs (several replicas booting) safe.
//
// It owns its own connection: the driver pins a connection for its lifetime
// and closes the *sql.DB on Close, so it must never share the app pool.
// Always call Close.
type Migrator struct{ m *migrate.Migrate }

func NewMigrator(url string) (*Migrator, error) {
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("migrate: open: %w", err)
	}
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate: source: %w", err)
	}
	drv, err := migratepgx.WithInstance(sqlDB, &migratepgx.Config{})
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate: driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", drv)
	if err != nil {
		_ = drv.Close()
		return nil, fmt.Errorf("migrate: init: %w", err)
	}
	return &Migrator{m: m}, nil
}

// Migrate is a convenience for "open, apply everything, close".
func Migrate(url string) error {
	m, err := NewMigrator(url)
	if err != nil {
		return err
	}
	defer m.Close()
	return m.Up()
}

func (mg *Migrator) Close() {
	if srcErr, dbErr := mg.m.Close(); srcErr != nil || dbErr != nil {
		slog.Warn("migrate: close", "source_err", srcErr, "db_err", dbErr)
	}
}

func (mg *Migrator) Up() error {
	if err := mg.m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	v, dirty, _ := mg.Version()
	slog.Info("migrations applied", "version", v, "dirty", dirty)
	return nil
}

// Down rolls back n migrations.
func (mg *Migrator) Down(n int) error {
	if err := mg.m.Steps(-n); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate down: %w", err)
	}
	return nil
}

func (mg *Migrator) Version() (uint, bool, error) {
	v, dirty, err := mg.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return v, dirty, err
}

// Force sets the version without running anything (recovery from a dirty state).
func (mg *Migrator) Force(v int) error { return mg.m.Force(v) }
