package main

import (
	"database/sql"
	"flag"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/entephemeral"
	_ "github.com/lightsparkdev/spark/so/entephemeral/runtime"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func openStartupDatabases(t *testing.T) (*ent.Client, *entephemeral.Client) {
	t.Helper()
	dir := t.TempDir()
	main, err := ent.Open("sqlite3", filepath.Join(dir, "main.db")+"?_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = main.Close() })
	ephemeral, err := entephemeral.Open("sqlite3", filepath.Join(dir, "ephemeral.db")+"?_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ephemeral.Close() })
	return main, ephemeral
}

func TestLocalSchemasCreateAndPreserveData(t *testing.T) {
	main, ephemeral := openStartupDatabases(t)
	checkLocalSchemasPreserveData(t, "sqlite3", main, ephemeral)
}

func checkLocalSchemasPreserveData(t *testing.T, driver string, main *ent.Client, ephemeral *entephemeral.Client) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, initializeLocalSchemas(ctx, true, driver, main, ephemeral))

	key := keys.GeneratePrivateKey()
	wallet, err := main.WalletSetting.Create().SetOwnerIdentityPublicKey(key.Public()).SetPrivateEnabled(true).Save(ctx)
	require.NoError(t, err)
	secret, err := ephemeral.SigningKeyshareSecret.Create().SetSigningKeyshareID(uuid.New()).SetVersion(1).SetSecretShare(key).Save(ctx)
	require.NoError(t, err)

	require.NoError(t, initializeLocalSchemas(ctx, true, driver, main, ephemeral))
	storedWallet, err := main.WalletSetting.Get(ctx, wallet.ID)
	require.NoError(t, err)
	require.True(t, storedWallet.PrivateEnabled)
	storedSecret, err := ephemeral.SigningKeyshareSecret.Get(ctx, secret.ID)
	require.NoError(t, err)
	require.True(t, key.Equals(storedSecret.SecretShare))
}

func TestLocalSchemasPostgres(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("SKIP_POSTGRES_TESTS is true")
	}
	t.Cleanup(db.StartPostgresServer())
	for _, fresh := range []bool{true, false} {
		name := "versioned migrations"
		if fresh {
			name = "empty databases"
		}
		t.Run(name, func(t *testing.T) {
			_, mainDB := db.ConnectToTestPostgres(t)
			_, ephemeralDB := db.ConnectToTestPostgres(t)
			reset := func(uri string) {
				conn, err := sql.Open("pgx", uri)
				require.NoError(t, err)
				defer conn.Close()
				_, err = conn.ExecContext(t.Context(), "DROP SCHEMA public CASCADE; CREATE SCHEMA public")
				require.NoError(t, err)
			}
			if fresh {
				reset(mainDB.DatabasePath())
			}
			reset(ephemeralDB.DatabasePath())
			ephemeral, err := entephemeral.Open("postgres", ephemeralDB.DatabasePath())
			require.NoError(t, err)
			t.Cleanup(func() { _ = ephemeral.Close() })
			checkLocalSchemasPreserveData(t, "postgres", mainDB.Client, ephemeral)
		})
	}
}

func TestLocalSchemasWithoutEphemeral(t *testing.T) {
	main, _ := openStartupDatabases(t)
	require.NoError(t, initializeLocalSchemas(t.Context(), true, "sqlite3", main, nil))
	_, err := main.WalletSetting.Query().Count(t.Context())
	require.NoError(t, err)
}

func TestNonLocalSchemasRemainUninitialized(t *testing.T) {
	main, ephemeral := openStartupDatabases(t)
	require.NoError(t, initializeLocalSchemas(t.Context(), false, "sqlite3", main, ephemeral))
	_, err := main.WalletSetting.Query().Count(t.Context())
	require.Error(t, err)
	_, err = ephemeral.SigningKeyshareSecret.Query().Count(t.Context())
	require.Error(t, err)
}

func TestLocalSchemaErrors(t *testing.T) {
	t.Run("main failure prevents ephemeral initialization", func(t *testing.T) {
		main, ephemeral := openStartupDatabases(t)
		require.NoError(t, main.Close())
		require.ErrorContains(t, initializeLocalSchemas(t.Context(), true, "sqlite3", main, ephemeral), "initialize main database schema")
		_, err := ephemeral.SigningKeyshareSecret.Query().Count(t.Context())
		require.Error(t, err)
	})
	t.Run("ephemeral failure is returned", func(t *testing.T) {
		main, ephemeral := openStartupDatabases(t)
		require.NoError(t, ephemeral.Close())
		require.ErrorContains(t, initializeLocalSchemas(t.Context(), true, "sqlite3", main, ephemeral), "initialize ephemeral database schema")
	})
}

func TestListenAddressFlag(t *testing.T) {
	for _, host := range []string{"", "127.0.0.1", "::1"} {
		t.Run("host="+host, func(t *testing.T) {
			oldFlags, oldArgs := flag.CommandLine, os.Args
			t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
			flag.CommandLine = flag.NewFlagSet("operator", flag.ContinueOnError)
			os.Args = []string{"operator", "-key", "test.key", "-operators", "operators.json", "-signer", "unix:///tmp/test.sock", "-http-port", "8535"}
			if host != "" {
				os.Args = append(os.Args, "-listen-address", host)
			}
			parsed, err := loadArgs()
			require.NoError(t, err)
			address := parsed.listenAddr(8535)
			gotHost, gotPort, err := net.SplitHostPort(address)
			require.NoError(t, err)
			require.Equal(t, host, gotHost)
			require.Equal(t, "8535", gotPort)
			if host == "127.0.0.1" {
				listener, err := net.Listen("tcp", parsed.listenAddr(0))
				require.NoError(t, err)
				defer listener.Close()
				require.Equal(t, host, listener.Addr().(*net.TCPAddr).IP.String())
			}
		})
	}
}
