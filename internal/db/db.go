// Package db owns the single Postgres connection pool shared by Ent, sqlc and
// (later) River and the session store.
package db

import (
	"context"
	"database/sql"
	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"rentmapgh/internal/ent"
	_ "rentmapgh/internal/ent/runtime" // registers schema defaults, hooks and privacy policies
)

type DB struct {
	Pool *pgxpool.Pool // native pgx: sqlc, River
	SQL  *sql.DB       // database/sql view over the same pool: Ent
	Ent  *ent.Client
}

func Open(ctx context.Context, url string, maxConns int32) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("db: parse url: %w", err)
	}
	cfg.MaxConns = maxConns

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}

	sqlDB := stdlib.OpenDBFromPool(pool)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))

	return &DB{Pool: pool, SQL: sqlDB, Ent: client}, nil
}

func (d *DB) Ping(ctx context.Context) error { return d.Pool.Ping(ctx) }

func (d *DB) Close() {
	_ = d.Ent.Close() // also closes d.SQL
	d.Pool.Close()
}
