package pg

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type config struct {
	pass    string
	user    string
	name    string
	port    uint32
	version string

	detached bool

	logger io.Writer

	migrationsFiles []string
	fixtureFiles    []string
}

var (
	supportedVersions = map[string]string{
		"10.3.2": "postgis/postgis:10-3.2-alpine",
		"11.2.5": "postgis/postgis:11-2.5-alpine",
		"11.3.2": "postgis/postgis:11-3.2-alpine",
		"12.3.2": "postgis/postgis:12-3.2-alpine",
		"13-3.1": "odidev/postgis:13-3.1-alpine",
		"13.3.2": "postgis/postgis:13-3.2-alpine",
		"14.3.2": "postgis/postgis:14-3.2-alpine",
	}
)

type Option func(*config) error

func WithHost(user, pass, name string, port uint32) Option {
	return func(c *config) error {
		c.user = user
		c.pass = pass
		c.name = name
		c.port = port
		return nil
	}
}

// WithVersion applied selected postgres version to config
func WithVersion(version string) Option {
	vv := strings.TrimSpace(version)
	return func(c *config) error {
		if vv == "" {
			c.version = "13-3.1"
			return nil
		}
		versions := getVersions()
		for _, v := range versions {
			if v == vv {
				c.version = version
				return nil
			}
		}
		return fmt.Errorf("seleced postgres version (%s) is not supported, select one of: %s", vv, strings.Join(versions, ","))
	}
}

func getVersions() []string {
	out := make([]string, 0)
	for k := range supportedVersions {
		out = append(out, k)
	}
	return out
}

func WithLogger(logger io.Writer) Option {
	return func(c *config) error {
		c.logger = logger
		return nil
	}
}

func WithMigrations(path string) Option {
	return func(c *config) error {
		files, err := getFiles(path)
		if err != nil {
			return fmt.Errorf("read migraions failed: %w", err)
		}

		for _, f := range files {
			// ignore migration down files
			if strings.HasSuffix(f, "down.sql") {
				continue
			}
			c.migrationsFiles = append(c.migrationsFiles, f)
		}

		return nil
	}
}

func WithFixtures(path string) Option {
	return func(c *config) error {
		files, err := getFiles(path)
		if err != nil {
			return fmt.Errorf("read fixtures failed: %w", err)
		}
		c.fixtureFiles = files
		return nil
	}
}

func getFiles(path string) ([]string, error) {
	if len(path) == 0 {
		return nil, nil
	}

	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("get path information failed, %w", err)
	}

	var out []string

	if !stat.IsDir() {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil, fmt.Errorf("file %s not exit", path)
		}
		out = append(out, path)
		return out, nil
	}

	files, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		out = append(out, filepath.Join(absPath, f.Name()))
	}

	sort.Strings(out)
	return out, nil
}

func getPostGisImage(version string) string {
	if v, ok := supportedVersions[version]; ok {
		return v
	}
	// fallback to odidev/postgis:13-3.1
	return "odidev/postgis:13-3.1-alpine"
}
