package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rupor-github/gencfg"
	yaml "gopkg.in/yaml.v3"
)

//go:embed config.yaml.tmpl
var ConfigTmpl []byte

type Config struct {
	Version    int              `yaml:"version" validate:"eq=1"`
	Database   DatabaseConfig   `yaml:"database"`
	Processing ProcessingConfig `yaml:"processing"`
	Output     OutputConfig     `yaml:"output"`
	Logging    LoggingConfig    `yaml:"logging"`
}

type DatabaseConfig struct {
	DSN              string `yaml:"dsn"`
	Host             string `yaml:"host" validate:"required"`
	Port             int    `yaml:"port" validate:"min=0,max=65535"`
	Protocol         string `yaml:"protocol" validate:"oneof=tcp unix"`
	User             string `yaml:"user" validate:"required"`
	Password         string `yaml:"password"`
	Name             string `yaml:"name" validate:"required"`
	Managed          bool   `yaml:"managed"`
	DataDir          string `yaml:"data_dir" validate:"required"`
	Temporary        bool   `yaml:"temporary"`
	OverwriteDataDir bool   `yaml:"overwrite_data_dir"`
	KeepRunning      bool   `yaml:"keep_running"`
	Socket           string `yaml:"socket"`
	PIDFile          string `yaml:"pid_file"`
	LogFile          string `yaml:"log_file"`
	ServerPath       string `yaml:"server_path,omitempty"`
	InstallDBPath    string `yaml:"install_db_path,omitempty"`
	ClientPath       string `yaml:"client_path,omitempty"`
	MaxOpenConns     int    `yaml:"max_open_connections" validate:"min=0"`
	MaxIdleConns     int    `yaml:"max_idle_connections" validate:"min=0"`
	ConnMaxLifetime  int    `yaml:"connection_max_lifetime_seconds" validate:"min=0"`
	Import           bool   `yaml:"import"`
	Create           bool   `yaml:"create"`
	DropBeforeImport bool   `yaml:"drop_before_import"`
}

type ProcessingConfig struct {
	Process              string   `yaml:"process" validate:"oneof=fb2 usr all"`
	ParseFB2             bool     `yaml:"parse_fb2"`
	DatabaseWorkers      int      `yaml:"database_workers" validate:"min=0"`
	DatabaseBatchSize    int      `yaml:"database_batch_size" validate:"min=1"`
	Archives             []string `yaml:"archives" validate:"dive,required"`
	OnlineWhenNoArchives bool     `yaml:"online_when_no_archives"`
}

type OutputConfig struct {
	JSONL string `yaml:"jsonl" validate:"required"`
}

func unmarshalConfig(data []byte, cfg *Config, process bool) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("failed to decode configuration data: %w", err)
	}
	if process {
		if err := gencfg.Sanitize(cfg); err != nil {
			return nil, fmt.Errorf("failed to sanitize configuration: %w", err)
		}
		if err := gencfg.Validate(cfg); err != nil {
			return nil, fmt.Errorf("failed to validate configuration: %w", err)
		}
	}
	return cfg, nil
}

func LoadConfiguration(path string, options ...func(*gencfg.ProcessingOptions)) (*Config, error) {
	haveFile := len(path) > 0
	options = append(defaultProcessingOptions(), options...)

	data, err := gencfg.Process(ConfigTmpl, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to process configuration template: %w", err)
	}
	cfg, err := unmarshalConfig(data, &Config{}, !haveFile)
	if err != nil {
		return nil, fmt.Errorf("failed to process configuration template: %w", err)
	}
	if !haveFile {
		return cfg, nil
	}

	data, err = os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	cfg, err = unmarshalConfig(data, cfg, haveFile)
	if err != nil {
		return nil, fmt.Errorf("failed to process configuration file: %w", err)
	}
	return cfg, nil
}

func Prepare() ([]byte, error) {
	return gencfg.Process(ConfigTmpl, defaultProcessingOptions()...)
}

func Dump(cfg *Config) ([]byte, error) {
	data, err := yaml.Marshal(*cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config to yaml: %w", err)
	}
	return data, nil
}

func defaultProcessingOptions() []func(*gencfg.ProcessingOptions) {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return []func(*gencfg.ProcessingOptions){gencfg.WithRootDir(filepath.Dir(exe))}
}
