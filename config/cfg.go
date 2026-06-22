package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	yaml "gopkg.in/yaml.v3"

	"github.com/rupor-github/gencfg"
)

//go:embed config.yaml.tmpl
var ConfigTmpl []byte

type Config struct {
	Version    int              `yaml:"version" validate:"eq=1"`
	Database   DatabaseConfig   `yaml:"database"`
	Fetch      FetchConfig      `yaml:"fetch"`
	Processing ProcessingConfig `yaml:"processing"`
	INPX       INPXConfig       `yaml:"inpx"`
	Logging    LoggingConfig    `yaml:"logging"`
}

type DatabaseConfig struct {
	DSN             string `yaml:"dsn"`
	Host            string `yaml:"host" validate:"required"`
	Port            int    `yaml:"port" validate:"min=0,max=65535"`
	Protocol        string `yaml:"protocol" validate:"oneof=tcp unix"`
	User            string `yaml:"user" validate:"required"`
	Password        string `yaml:"password"`
	Name            string `yaml:"name" validate:"required"`
	Managed         bool   `yaml:"managed"`
	DataDir         string `yaml:"data_dir" validate:"required"`
	Temporary       bool   `yaml:"temporary"`
	Socket          string `yaml:"socket"`
	PIDFile         string `yaml:"pid_file"`
	LogFile         string `yaml:"log_file"`
	ServerPath      string `yaml:"server_path,omitempty"`
	InstallDBPath   string `yaml:"install_db_path,omitempty"`
	ClientPath      string `yaml:"client_path,omitempty"`
	MaxOpenConns    int    `yaml:"max_open_connections" validate:"min=0"`
	MaxIdleConns    int    `yaml:"max_idle_connections" validate:"min=0"`
	ConnMaxLifetime int    `yaml:"connection_max_lifetime_seconds" validate:"min=0"`
}

type ProcessingConfig struct {
	ParseFB2           bool           `yaml:"parse_fb2"`
	FB2DescriptionTree bool           `yaml:"fb2_description_tree"`
	ArchiveContentMD5  bool           `yaml:"archive_content_md5"`
	Manifests          ManifestConfig `yaml:"manifests"`
	DatabaseWorkers    int            `yaml:"database_workers" validate:"min=0"`
	DatabaseBatchSize  int            `yaml:"database_batch_size" validate:"min=1"`
	ArchiveWorkers     int            `yaml:"archive_workers" validate:"min=0"`
	ArchiveBatchSize   int            `yaml:"archive_batch_size" validate:"min=1"`
	ArchiveReadBuffer  int            `yaml:"archive_read_buffer_size" validate:"min=0"`
	Rebuild            bool           `yaml:"-"`
}

type FetchConfig struct {
	Libraries []FetchLibraryConfig `yaml:"libraries" validate:"dive"`
}

type FetchLibraryConfig struct {
	Name            string `yaml:"name" validate:"required"`
	LibraryName     string `yaml:"library_name"`
	ArchivePattern  string `yaml:"archive_pattern" validate:"required"`
	SQLPattern      string `yaml:"sql_pattern" validate:"required"`
	ArchiveURL      string `yaml:"archive_url" validate:"required,url"`
	SQLURL          string `yaml:"sql_url" validate:"required,url"`
	Proxy           string `yaml:"proxy" validate:"omitempty,url"`
	UserAgentSuffix string `yaml:"user_agent_suffix"`
}

func (c *FetchConfig) FindLibrary(name string) (FetchLibraryConfig, bool) {
	for _, lib := range c.Libraries {
		if lib.Name == name {
			return lib, true
		}
	}
	return FetchLibraryConfig{}, false
}

type ManifestConfig struct {
	ArchiveDir  string `yaml:"archive_dir"`
	DatabaseDir string `yaml:"database_dir"`
}

type INPXConfig struct {
	QuickFix        bool       `yaml:"quick_fix"`
	CommentTemplate string     `yaml:"comment_template"`
	Limits          INPXLimits `yaml:"limits"`
}

type INPXLimits struct {
	AuthorName   int `yaml:"author_name" validate:"min=1"`
	AuthorMiddle int `yaml:"author_middle" validate:"min=1"`
	AuthorFamily int `yaml:"author_family" validate:"min=1"`
	Title        int `yaml:"title" validate:"min=1"`
	Keywords     int `yaml:"keywords" validate:"min=1"`
	Sequence     int `yaml:"sequence" validate:"min=1"`
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
