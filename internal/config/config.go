package config

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is everything the CLI needs to know before it starts.
type Config struct {
	Dir        string // directory to serve
	LocalPort  int    // 0 = pick a free port automatically
	RelayAddr  string // host:port of the akpa relay's control listener
	NoBanner   bool
	ForceDL    bool // force downloads instead of rendering in the browser
	ShowVer    bool
	ConfigPath string // where config was loaded from, for diagnostics
}

// Defaults. Anything a user is likely to override lives here, not scattered
// through main() as literals.
const (
	DefaultRelay = "akpa.victorabuka.com:7000"
	DefaultDir   = "."
)

// Load resolves configuration from four sources, in increasing priority:
//
//	1. built-in defaults
//	2. config file   (~/.config/akpa/config OR ./.env when it exists)
//	3. environment   (AKPA_RELAY, AKPA_PORT, …)
//	4. command-line flags
//
// Later sources win. This ordering is the standard one and it is worth
// getting right: a user should always be able to override a config file for
// one run without editing it.
func Load(args []string) (*Config, error) {
	cfg := &Config{
		Dir:       DefaultDir,
		RelayAddr: DefaultRelay,
	}

	// --- 2. config file -----------------------------------------------------
	if path, vals := loadFile(); path != "" {
		cfg.ConfigPath = path
		applyMap(cfg, vals)
	}

	// --- 3. environment -----------------------------------------------------
	applyMap(cfg, envMap())

	// --- 4. flags -----------------------------------------------------------
	fs := flag.NewFlagSet("akpa", flag.ContinueOnError)
	fs.StringVar(&cfg.RelayAddr, "relay", cfg.RelayAddr, "akpa relay address (host:port)")
	fs.IntVar(&cfg.LocalPort, "port", cfg.LocalPort, "local port for the file server (0 = auto)")
	fs.BoolVar(&cfg.NoBanner, "no-banner", cfg.NoBanner, "suppress the startup banner")
	fs.BoolVar(&cfg.ForceDL, "download", cfg.ForceDL, "force files to download instead of rendering")
	fs.BoolVar(&cfg.ShowVer, "version", false, "print version and exit")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `akpa — share a folder over one link

usage:
  akpa [flags] [directory]

flags:
`)
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
environment:
  AKPA_RELAY       relay address (default %s)
  AKPA_PORT        local port
  AKPA_NO_BANNER   set to any value to hide the banner
  NO_COLOR         set to any value to disable colored output

config file:
  ~/.config/akpa/config    KEY=value, one per line

examples:
  akpa                                  serve the current directory
  akpa ~/photos                         serve a specific directory
  akpa --relay localhost:8080 .         point at a local relay for development
`, DefaultRelay)
	}

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// A bare positional argument is the directory. Flags are parsed first so
	// `akpa --port 9000 ~/photos` works the way people expect.
	if fs.NArg() > 0 {
		cfg.Dir = fs.Arg(0)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	abs, err := filepath.Abs(c.Dir)
	if err != nil {
		return fmt.Errorf("resolving %q: %w", c.Dir, err)
	}
	c.Dir = abs

	info, err := os.Stat(c.Dir)
	if os.IsNotExist(err) {
		return fmt.Errorf("no such directory: %s", c.Dir)
	}
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", c.Dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is a file, not a directory", c.Dir)
	}

	if c.RelayAddr == "" {
		return fmt.Errorf("relay address is empty")
	}
	if !strings.Contains(c.RelayAddr, ":") {
		return fmt.Errorf("relay address %q needs a port, e.g. %s", c.RelayAddr, DefaultRelay)
	}
	if c.LocalPort < 0 || c.LocalPort > 65535 {
		return fmt.Errorf("port %d is out of range", c.LocalPort)
	}
	return nil
}

// ---------------------------------------------------------------------------
// sources
// ---------------------------------------------------------------------------

func envMap() map[string]string {
	m := map[string]string{}
	for _, k := range []string{"AKPA_RELAY", "AKPA_PORT", "AKPA_NO_BANNER", "AKPA_DOWNLOAD"} {
		if v, ok := os.LookupEnv(k); ok {
			m[k] = v
		}
	}
	return m
}

// loadFile reads ~/.config/akpa/config, falling back to ./.env only when
// AKPA_DEV is set.
//
// Reading ./.env unconditionally would be a real bug in a CLI: akpa runs in
// whatever directory the user is sharing. If that folder happens to contain
// someone else's .env — a cloned repo, a downloads folder — you would silently
// import their settings. Config for an installed binary belongs in the user's
// config directory, not the working directory.
func loadFile() (string, map[string]string) {
	if os.Getenv("AKPA_DEV") != "" {
		if vals, err := parseEnvFile(".env"); err == nil {
			return ".env", vals
		}
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return "", nil
	}
	path := filepath.Join(dir, "akpa", "config")
	vals, err := parseEnvFile(path)
	if err != nil {
		return "", nil
	}
	return path, vals
}

// parseEnvFile reads KEY=value lines. Blank lines and # comments are skipped;
// surrounding quotes are stripped.
//
// This is ~25 lines and removes a dependency. godotenv is fine on the server,
// where you control the working directory — here it buys little.
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		vals[key] = val
	}
	return vals, sc.Err()
}

func applyMap(cfg *Config, m map[string]string) {
	if v, ok := m["AKPA_RELAY"]; ok && v != "" {
		cfg.RelayAddr = v
	}
	if v, ok := m["AKPA_PORT"]; ok && v != "" {
		var p int
		if _, err := fmt.Sscanf(v, "%d", &p); err == nil {
			cfg.LocalPort = p
		}
	}
	if v, ok := m["AKPA_NO_BANNER"]; ok && v != "" {
		cfg.NoBanner = true
	}
	if v, ok := m["AKPA_DOWNLOAD"]; ok && v != "" {
		cfg.ForceDL = true
	}
}
