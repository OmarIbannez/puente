// Package storetest starts a throwaway PostgreSQL container for package
// tests. Start one per test package from TestMain and terminate it at exit.
package storetest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Image is the PostgreSQL image used by tests.
const Image = "postgres:17-alpine"

// Postgres is a running test database server.
type Postgres struct {
	container *postgres.PostgresContainer
	// URL is the connection string of the default "puente" database.
	URL string
}

// Start runs a PostgreSQL container and waits until it accepts connections.
// When DATABASE_TEST_URL is set, it is used instead and no container starts.
func Start(ctx context.Context) (*Postgres, error) {
	if u := os.Getenv("DATABASE_TEST_URL"); u != "" {
		return &Postgres{URL: u}, nil
	}
	c, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("puente"),
		postgres.WithUsername("puente"),
		postgres.WithPassword("puente"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		if c != nil {
			_ = testcontainers.TerminateContainer(c)
		}
		return nil, fmt.Errorf("storetest: start %s: %w", Image, err)
	}
	connURL, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = testcontainers.TerminateContainer(c)
		return nil, fmt.Errorf("storetest: connection string: %w", err)
	}
	return &Postgres{container: c, URL: connURL}, nil
}

// Terminate stops and removes the container (a no-op for DATABASE_TEST_URL).
func (p *Postgres) Terminate() error {
	if p.container == nil {
		return nil
	}
	return testcontainers.TerminateContainer(p.container)
}

// CreateDatabase creates an empty database on the server and returns its
// connection string. The name gets a unique suffix.
func (p *Postgres) CreateDatabase(ctx context.Context, prefix string) (string, error) {
	name := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	conn, err := pgx.Connect(ctx, p.URL)
	if err != nil {
		return "", fmt.Errorf("storetest: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		return "", fmt.Errorf("storetest: create database %s: %w", name, err)
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return "", fmt.Errorf("storetest: parse url: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// Main starts a server, opens a migrated pool on it, stores the pool in
// *pool and runs the package's tests. Call it from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }
func Main(m *testing.M, pool **pgxpool.Pool) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pg, err := Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = pg.Terminate() }()
	p, err := store.Open(ctx, pg.URL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer p.Close()
	if err := store.Migrate(ctx, p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	*pool = p
	return m.Run()
}

// Sandbox creates a fresh visitor sandbox for one test.
func Sandbox(t testing.TB, pool *pgxpool.Pool) store.Sandbox {
	t.Helper()
	s, err := store.CreateSandbox(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
