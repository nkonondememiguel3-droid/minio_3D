package db

import (
	"embed"
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Connect opens a PostgreSQL connection pool and runs all pending migrations.
func Connect(dsn string) (*sqlx.DB, error) {
	database, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		fmt.Println(database)
		return nil, fmt.Errorf("db connect: %w", err)
	}

	// Sensible pool settings for a stateless API server
	database.SetMaxOpenConns(25)
	database.SetMaxIdleConns(5)

	if err := runMigrations(database); err != nil {
		return nil, fmt.Errorf("db migrate: %w", err)
	}

	return database, nil
}

// runMigrations applies every *.sql file found in the migrations embed.
// This is a simple ordered approach sufficient for Phase 1.
// In Phase 2+, replace with golang-migrate or goose.
func runMigrations(db *sqlx.DB) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		sql, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		if _, err := db.Exec(string(sql)); err != nil {
			return fmt.Errorf("exec migration %s: %w", entry.Name(), err)
		}
	}

	return nil
}
