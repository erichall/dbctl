package pg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	// golang postgres driver
	_ "github.com/lib/pq"
	"github.com/mirzakhany/dbctl/internal/container"
	"github.com/mirzakhany/dbctl/internal/database"
)

var (
	_ database.Database = (*Postgres)(nil)
	_ database.Admin    = (*Postgres)(nil)

	errDatabaseNotExists = errors.New("database does not exist")
)

const (
	DefaultPort = 15432
	DefaultUser = "postgres"
	DefaultPass = "postgres"
	DefaultName = "postgres"

	DefaultTemplate = "dbctl_template"
)

type Postgres struct {
	containerID string
	cfg         config
}

func New(options ...Option) (*Postgres, error) {
	// create postgres with default values
	pg := &Postgres{cfg: config{
		pass:    "postgres",
		user:    "postgres",
		name:    "postgres",
		port:    DefaultPort,
		version: "14.3.0",
	}}

	for _, o := range options {
		if err := o(&pg.cfg); err != nil {
			return nil, err
		}
	}

	return pg, nil
}

func (p *Postgres) CreateDB(ctx context.Context, req *database.CreateDBRequest) (*database.CreateDBResponse, error) {
	t1 := time.Now()
	// connect to default database
	conn, err := dbConnect(ctx, p.URI())
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = conn.Close()
	}()
	t2 := time.Now()
	fmt.Println("open connection", t2.Sub(t1))

	// create a random name for new database
	dbName := fmt.Sprintf("dbctl_%d", time.Now().UnixNano())
	newDB, _ := New(WithHost(p.cfg.user, p.cfg.pass, dbName, p.cfg.port))
	newURI := newDB.URI()

	// create database
	// if default is exist, use it as template and create new database
	if err := p.createDatabaseWithTemplate(ctx, conn, dbName, DefaultTemplate); err == nil {
		log.Println("database created using template")
		t3 := time.Now()
		fmt.Println("create db", t3.Sub(t2))
		return &database.CreateDBResponse{URI: newURI}, nil
	} else {
		if !errors.Is(err, errDatabaseNotExists) {
			return nil, err
		}
	}

	if _, err := conn.Exec(fmt.Sprintf("create database %s", dbName)); err != nil {
		return nil, err
	}

	if len(req.Migrations) != 0 {
		// run migrations if exist
		migrationFiles, err := getFiles(req.Migrations)
		if err != nil {
			return nil, fmt.Errorf("read migraions failed: %w", err)
		}
		if err := RunMigrations(ctx, migrationFiles, newURI); err != nil {
			return nil, err
		}
	}

	// create template database
	_ = p.createDatabaseWithTemplate(ctx, conn, DefaultTemplate, dbName)

	if len(req.Fixtures) != 0 {
		// run apply fixtures if exist
		fixtureFiles, err := getFiles(req.Fixtures)
		if err != nil {
			return nil, fmt.Errorf("read fixtures failed: %w", err)
		}
		if err := ApplyFixtures(ctx, fixtureFiles, newURI); err != nil {
			return nil, err
		}
	}

	return &database.CreateDBResponse{URI: newDB.URI()}, nil
}

func (p *Postgres) createDatabaseWithTemplate(ctx context.Context, conn *sql.DB, name, template string) error {
	if conn == nil {
		var err error
		conn, err = dbConnect(ctx, p.URI())
		if err != nil {
			return err
		}
		defer func() {
			_ = conn.Close()
		}()
	}

	// if default is exist, use it as template and create new database
	if _, err := conn.Exec(fmt.Sprintf("create database %s with template %s", name, template)); err != nil {
		// is error database not exist?
		if strings.Contains(err.Error(), "does not exist") {
			return errDatabaseNotExists
		}
		return err
	}
	return nil
}

func (p *Postgres) RemoveDB(ctx context.Context, uri string) error {
	// parse the uri to get database name
	u, err := url.Parse(uri)
	if err != nil {
		return err
	}

	// get database name
	dbName := strings.TrimPrefix(u.Path, "/")

	conn, err := dbConnect(ctx, p.URI())
	if err != nil {
		return err
	}
	defer func() {
		_ = conn.Close()
	}()

	// terminate connection
	_, _ = conn.ExecContext(ctx, "select pg_terminate_backend(pid) from pg_stat_activity where datname = $1", dbName)
	if _, err := conn.ExecContext(ctx, "drop database if exists $1", dbName); err != nil {
		return fmt.Errorf("drop database failed: %v", err)
	}

	return nil
}

func (p *Postgres) Start(ctx context.Context, detach bool) error {
	log.Printf("Starting postgres version %s on port %d ...\n", p.cfg.version, p.cfg.port)

	closeFunc, err := p.startUsingDocker(ctx, 20*time.Second)
	if err != nil {
		return err
	}

	log.Println("Postgres is up and running")
	// run migrations if exist
	if err := RunMigrations(ctx, p.cfg.migrationsFiles, p.URI()); err != nil {
		return err
	}

	// create template database if migrations exist
	if len(p.cfg.migrationsFiles) > 0 {
		_ = p.createDatabaseWithTemplate(ctx, nil, DefaultTemplate, p.cfg.name)
	}

	// run apply fixtures if exist
	if err := ApplyFixtures(ctx, p.cfg.fixtureFiles, p.URI()); err != nil {
		return err
	}

	// print connection url
	log.Printf("Database uri is: %q\n", p.URI())

	var pgwebCloseFunc database.CloseFunc
	if p.cfg.withUI {
		pgwebCloseFunc, err = p.runUI(ctx)
		if err != nil {
			_ = closeFunc(ctx)
			return err
		}
	}

	// detach and stop cli if asked
	if detach {
		return nil
	}

	<-ctx.Done()
	log.Println("Shutdown signal received, stopping database")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() {
		cancel()
	}()

	// TODO we need a better solution to manage containers and make sure we remove all of them.
	if pgwebCloseFunc != nil {
		if err := pgwebCloseFunc(shutdownCtx); err != nil {
			return err
		}
	}

	return closeFunc(shutdownCtx)
}

func (p *Postgres) Stop(ctx context.Context) error {
	return container.TerminateByID(ctx, p.containerID)
}

func (p *Postgres) WaitForStart(ctx context.Context, timeout time.Duration) error {
	log.Println("Wait for database to boot up")
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for range ticker.C {
		conn, err := dbConnect(ctx, p.URI())
		if err != nil {
			if err == context.DeadlineExceeded {
				return err
			}
		} else {
			_ = conn.Close()
			return nil
		}
	}
	return nil
}

func (p *Postgres) runUI(ctx context.Context) (database.CloseFunc, error) {
	log.Println("Starting postgres ui using pgweb (https://github.com/sosedoff/pgweb)")

	var rnd, err = rand.Int(rand.Reader, big.NewInt(20))
	if err != nil {
		return nil, err
	}

	pgweb, err := container.Run(ctx, container.CreateRequest{
		Image: "sosedoff/pgweb:latest",
		Env: map[string]string{
			// replace localhost with docker internal network
			"PGWEB_DATABASE_URL": strings.ReplaceAll(p.URI(), "localhost", "host.docker.internal"),
		},
		ExposedPorts: []string{"8081:8081"},
		Name:         fmt.Sprintf("dbctl_pgweb_%d_%d", time.Now().Unix(), rnd.Uint64()),
		Labels:       map[string]string{database.LabelType: database.LabelPGWeb},
	})
	if err != nil {
		return nil, err
	}

	// log ui url
	log.Println("Database UI is running on: http://localhost:8081")

	closeFunc := func(ctx context.Context) error {
		return pgweb.Terminate(ctx)
	}

	return closeFunc, nil
}

func Instances(ctx context.Context) ([]database.Info, error) {
	l, err := container.List(ctx, map[string]string{database.LabelType: database.LabelPostgres})
	if err != nil {
		return nil, err
	}

	out := make([]database.Info, 0, len(l))
	for _, c := range l {
		out = append(out, database.Info{
			ID:     c.ID,
			Type:   c.Name,
			Status: database.Running,
		})
	}
	return out, nil
}

func (p *Postgres) startUsingDocker(ctx context.Context, timeout time.Duration) (database.CloseFunc, error) {
	var rnd, err = rand.Int(rand.Reader, big.NewInt(20))
	if err != nil {
		return nil, err
	}

	port := strconv.Itoa(int(p.cfg.port))
	pg, err := container.Run(ctx, container.CreateRequest{
		Image: getPostGisImage(p.cfg.version),
		Env: map[string]string{
			"POSTGRES_PASSWORD": p.cfg.pass,
			"POSTGRES_USER":     p.cfg.user,
			"POSTGRES_DB":       p.cfg.name,
		},
		Cmd:          []string{"postgres", "-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off"},
		ExposedPorts: []string{fmt.Sprintf("%s:5432/tcp", port)},
		Name:         fmt.Sprintf("dbctl_pg_%d_%d", time.Now().Unix(), rnd.Uint64()),
		Labels:       map[string]string{database.LabelType: database.LabelPostgres},
	})
	if err != nil {
		return nil, err
	}

	p.containerID = pg.ID

	closeFunc := func(ctx context.Context) error {
		return pg.Terminate(ctx)
	}

	return closeFunc, p.WaitForStart(ctx, timeout)
}

func (p *Postgres) URI() string {
	host := net.JoinHostPort("localhost", strconv.Itoa(int(p.cfg.port)))
	return (&url.URL{Scheme: "postgres", User: url.UserPassword(p.cfg.user, p.cfg.pass), Host: host, Path: p.cfg.name, RawQuery: "sslmode=disable"}).String()
}

func RunMigrations(ctx context.Context, migrationsFiles []string, uri string) error {
	if migrationsFiles == nil {
		return nil
	}

	log.Println("Applying migrations ...")
	return applySQL(ctx, migrationsFiles, uri)
}

func ApplyFixtures(ctx context.Context, fixtureFiles []string, uri string) error {
	if len(fixtureFiles) == 0 {
		return nil
	}

	log.Println("Applying fixtures ...")
	return applySQL(ctx, fixtureFiles, uri)
}

func applySQL(ctx context.Context, stmts []string, uri string) error {
	conn, err := dbConnect(ctx, uri)
	if err != nil {
		return fmt.Errorf("unable to connect to database: %w", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	for _, f := range stmts {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read file (%s) failed: %w", f, err)
		}

		if _, err := conn.Exec(string(b)); err != nil {
			return fmt.Errorf("applying file (%s) failed: %w", f, err)
		}
	}
	return nil
}

func dbConnect(ctx context.Context, uri string) (*sql.DB, error) {
	conn, err := sql.Open("postgres", uri)
	if err != nil {
		return nil, err
	}

	//if err := conn.PingContext(ctx); err != nil {
	//	return nil, err
	//}
	return conn, nil
}
