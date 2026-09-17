package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"unicode/utf8"

	atlas "ariga.io/atlas/sql/schema"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql/schema"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/migrate"
	"github.com/lightsparkdev/spark/so/entephemeral"
	ephemeralmigrate "github.com/lightsparkdev/spark/so/entephemeral/migrate"
)

func (a *args) listenAddr(port uint64) string {
	return net.JoinHostPort(a.ListenAddress, strconv.FormatUint(port, 10))
}

// initializeLocalSchemas uses Ent's additive, idempotent schema creation only
// for local runs. Production deployments continue to use versioned migrations.
func initializeLocalSchemas(ctx context.Context, local bool, driver string, main *ent.Client, ephemeral *entephemeral.Client) error {
	if !local {
		return nil
	}
	var opts []schema.MigrateOption
	if driver == dialect.Postgres {
		opts = append(opts, schema.WithDiffHook(postgresIndexNames))
	}
	// Ent configures these definitions for the selected dialect in place.
	// Keep the generated definitions intact for subsequent initializations.
	mainTables, err := schema.CopyTables(migrate.Tables)
	if err != nil {
		return fmt.Errorf("copy main database schema: %w", err)
	}
	if err := migrate.Create(ctx, main.Schema, mainTables, opts...); err != nil {
		return fmt.Errorf("initialize main database schema: %w", err)
	}
	if ephemeral != nil {
		ephemeralTables, err := schema.CopyTables(ephemeralmigrate.Tables)
		if err != nil {
			return fmt.Errorf("copy ephemeral database schema: %w", err)
		}
		if err := ephemeralmigrate.Create(ctx, ephemeral.Schema, ephemeralTables, opts...); err != nil {
			return fmt.Errorf("initialize ephemeral database schema: %w", err)
		}
	}
	return nil
}

// Ent v0.14.3 bounds explicit index names, but not the implicit unique indexes
// it creates for columns. Match PostgreSQL's 63-byte identifier truncation (also
// used by our versioned migrations) so a restart doesn't try to recreate them.
func postgresIndexNames(next schema.Differ) schema.Differ {
	return schema.DiffFunc(func(current, desired *atlas.Schema) ([]atlas.Change, error) {
		for _, table := range desired.Tables {
			for _, index := range table.Indexes {
				if len(index.Name) > 63 {
					index.Name = index.Name[:63]
					for !utf8.ValidString(index.Name) {
						index.Name = index.Name[:len(index.Name)-1]
					}
				}
			}
		}
		return next.Diff(current, desired)
	})
}
