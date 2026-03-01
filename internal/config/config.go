package config

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Identity Identity `yaml:"identity"`
	SMTP     SMTP     `yaml:"smtp"`
	IMAP     IMAP     `yaml:"imap"`
}

type Identity struct {
	FirstName string `yaml:"first_name"`
	LastName  string `yaml:"last_name"`
	Email     string `yaml:"email"`
	Phone     string `yaml:"phone"`
	Address   string `yaml:"address"`
	Country   string `yaml:"country"`
}

type SMTP struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

type IMAP struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Since    string `yaml:"since"` // e.g. "2025-01-01", optional
}

// Load reads the YAML config file and resolves ${ENV_VAR} patterns.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	cfg.SMTP.Password = resolveEnv(cfg.SMTP.Password)
	cfg.IMAP.Password = resolveEnv(cfg.IMAP.Password)

	return &cfg, nil
}

// resolveEnv replaces ${ENV_VAR} patterns with the corresponding environment variable value.
func resolveEnv(val string) string {
	if strings.HasPrefix(val, "${") && strings.HasSuffix(val, "}") {
		return os.Getenv(val[2 : len(val)-1])
	}
	return val
}
