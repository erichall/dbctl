package pg

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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

	migrationsPath string
	fixtureFiles   []string
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
		if len(path) == 0 {
			return nil
		}

		if _, err := os.Stat(path); os.IsNotExist(err) {
			return fmt.Errorf("migraions path %s not exit", path)
		}

		absPath, err := filepath.Abs(path)
		if err != nil {
			return err
		}

		c.migrationsPath = "file://" + absPath
		return nil
	}
}

func WithFixtures(path string) Option {
	return func(c *config) error {
		if len(path) == 0 {
			return nil
		}

		stat, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("get fixture path information failed, %w", err)
		}

		if !stat.IsDir() {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return fmt.Errorf("fixture file %s not exit", path)
			}
			c.fixtureFiles = append(c.fixtureFiles, path)
			return nil
		}

		files, err := os.ReadDir(path)
		if err != nil {
			return err
		}

		absPath, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		for _, f := range files {
			// TODO check fixtures file for format and template
			c.fixtureFiles = append(c.fixtureFiles, filepath.Join(absPath, f.Name()))
		}
		return nil
	}
}

func getPostGisImage(version string) string {
	if v, ok := supportedVersions[version]; ok {
		return v
	}
	// fallback to odidev/postgis:13-3.1
	return "odidev/postgis:13-3.1-alpine"
}
