package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	yaml "gopkg.in/yaml.v3"

	"github.com/rupor-github/gencfg"
)

//go:embed config.yaml.tmpl
var ConfigTmpl []byte

const (
	commentTemplateFieldName = "comment_template"
	versionTemplateFieldName = "version_template"

	commentTemplatePlaceholder = "__METABIB_DUMPCONFIG_COMMENT_TEMPLATE_8D4F1F06__"
	versionTemplatePlaceholder = "__METABIB_DUMPCONFIG_VERSION_TEMPLATE_6B0186F8__"
)

type Config struct {
	Version    int              `yaml:"version" validate:"eq=1"`
	Database   DatabaseConfig   `yaml:"database"`
	Fetch      FetchConfig      `yaml:"fetch"`
	Rollup     RollupConfig     `yaml:"rollup"`
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
	AdminPath       string `yaml:"admin_path,omitempty"`
	MaxOpenConns    int    `yaml:"max_open_connections" validate:"min=0"`
	MaxIdleConns    int    `yaml:"max_idle_connections" validate:"min=0"`
	ConnMaxLifetime int    `yaml:"connection_max_lifetime_seconds" validate:"min=0"`
}

type ProcessingConfig struct {
	ParseFB2                bool                          `yaml:"parse_fb2"`
	FB2DescriptionTree      bool                          `yaml:"fb2_description_tree"`
	FB2BodyFingerprints     bool                          `yaml:"fb2_body_fingerprints"`
	ArchiveContentMD5       bool                          `yaml:"archive_content_md5"`
	NestedArchiveInspection NestedArchiveInspectionConfig `yaml:"nested_archive_inspection"`
	Manifests               ManifestConfig                `yaml:"manifests"`
	DatabaseWorkers         int                           `yaml:"database_workers" validate:"min=0"`
	DatabaseBatchSize       int                           `yaml:"database_batch_size" validate:"min=1"`
	ArchiveWorkers          int                           `yaml:"archive_workers" validate:"min=0"`
	ArchiveBatchSize        int                           `yaml:"archive_batch_size" validate:"min=1"`
	ArchiveReadBuffer       int                           `yaml:"archive_read_buffer_size" validate:"min=0"`
	Rebuild                 bool                          `yaml:"-"`
}

type NestedArchiveInspectionConfig struct {
	Enabled              bool  `yaml:"enabled"`
	MaxCompressedSizeMiB int64 `yaml:"max_compressed_size_mib" validate:"min=0"`
}

func (c NestedArchiveInspectionConfig) MaxCompressedBytes() int64 {
	if c.MaxCompressedSizeMiB <= 0 {
		return c.MaxCompressedSizeMiB
	}
	const mib = int64(1024 * 1024)
	if c.MaxCompressedSizeMiB > (1<<63-1)/mib {
		return 1<<63 - 1
	}
	return c.MaxCompressedSizeMiB * mib
}

type FetchConfig struct {
	Libraries []FetchLibraryConfig `yaml:"libraries" validate:"dive"`
}

type RollupConfig struct {
	ValidateCRC    bool                        `yaml:"validate_crc"`
	Finalization   RollupFinalizationConfig    `yaml:"finalization"`
	UpdatePatterns []RollupUpdatePatternConfig `yaml:"update_patterns" validate:"dive"`
}

type RollupFinalizationConfig struct {
	Policy   string                           `yaml:"policy" validate:"omitempty,oneof=size rolling calendar"`
	Size     RollupSizeFinalizationConfig     `yaml:"size"`
	Rolling  RollupRollingFinalizationConfig  `yaml:"rolling"`
	Calendar RollupCalendarFinalizationConfig `yaml:"calendar"`
}

type RollupSizeFinalizationConfig struct {
	TargetMiB RollupTargetSizeConfig `yaml:"target_mib"`
}

type RollupTargetSizeConfig struct {
	FB2 int64 `yaml:"fb2"`
	USR int64 `yaml:"usr"`
}

type RollupRollingFinalizationConfig struct {
	Duration string `yaml:"duration"`
}

type RollupCalendarFinalizationConfig struct {
	Bucket string `yaml:"bucket" validate:"omitempty,oneof=iso-week iso-biweek month"`
}

type RollupUpdatePatternConfig struct {
	Name    string `yaml:"name"`
	Family  string `yaml:"family" validate:"required,oneof=fb2 usr"`
	Pattern string `yaml:"pattern" validate:"required"`
}

type FetchLibraryConfig struct {
	Name            string `yaml:"name" validate:"required"`
	LibraryName     string `yaml:"library_name"`
	ArchiveContent  string `yaml:"archive_content" validate:"omitempty,oneof=fb2 usr all"`
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
	QuickFix            bool               `yaml:"quick_fix"`
	DisambiguateAuthors bool               `yaml:"disambiguate_authors"`
	CommentTemplate     string             `yaml:"comment_template"`
	VersionTemplate     string             `yaml:"version_template"`
	Limits              INPXLimits         `yaml:"limits"`
	Language            INPXLanguageConfig `yaml:"language"`
	FLibrary            FLibraryINPXConfig `yaml:"flibrary"`
}

type INPXLanguageConfig struct {
	Canonicalize    bool                     `yaml:"canonicalize"`
	Aliases         map[string]string        `yaml:"aliases"`
	FallbackLocales []string                 `yaml:"fallback_locales" validate:"dive,required"`
	IgnorePatterns  []string                 `yaml:"ignore_patterns" validate:"dive,required"`
	ContextRules    []INPXLanguageRuleConfig `yaml:"context_rules"`
}

type INPXLanguageRuleConfig struct {
	From                  string   `yaml:"from" validate:"required"`
	To                    string   `yaml:"to" validate:"required"`
	WhenAnySourceLanguage []string `yaml:"when_any_source_language" validate:"min=1,dive,required"`
}

type INPXLimits struct {
	AuthorName   int `yaml:"author_name" validate:"min=1"`
	AuthorMiddle int `yaml:"author_middle" validate:"min=1"`
	AuthorFamily int `yaml:"author_family" validate:"min=1"`
	Title        int `yaml:"title" validate:"min=1"`
	Keywords     int `yaml:"keywords" validate:"min=1"`
	Sequence     int `yaml:"sequence" validate:"min=1"`
}

type FLibraryINPXConfig struct {
	SequenceDedup    string `yaml:"sequence_dedup" validate:"oneof=case-insensitive case-sensitive"`
	FB2PathSeparator string `yaml:"fb2_path_separator" validate:"required"`
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
		if err := validateConfig(cfg); err != nil {
			return nil, fmt.Errorf("failed to validate configuration: %w", err)
		}
	}
	return cfg, nil
}

func validateConfig(cfg *Config) error {
	if cfg.Processing.FB2BodyFingerprints && !cfg.Processing.ParseFB2 {
		return errors.New("processing.fb2_body_fingerprints requires processing.parse_fb2")
	}
	if err := validateRollupFinalizationConfig(&cfg.Rollup.Finalization); err != nil {
		return err
	}
	for i := range cfg.Fetch.Libraries {
		if cfg.Fetch.Libraries[i].ArchiveContent == "" {
			cfg.Fetch.Libraries[i].ArchiveContent = "fb2"
		}
	}
	return nil
}

func validateRollupFinalizationConfig(cfg *RollupFinalizationConfig) error {
	if cfg.Policy == "" {
		cfg.Policy = "size"
	}
	switch cfg.Policy {
	case "size":
		if cfg.Size.TargetMiB.FB2 <= 0 {
			return errors.New("rollup.finalization.size.target_mib.fb2 must be positive")
		}
		if cfg.Size.TargetMiB.USR <= 0 {
			return errors.New("rollup.finalization.size.target_mib.usr must be positive")
		}
	case "rolling":
		if _, err := parseRollupRollingDuration(cfg.Rolling.Duration); err != nil {
			return fmt.Errorf("rollup.finalization.rolling.duration is invalid: %w", err)
		}
	case "calendar":
		if cfg.Calendar.Bucket == "" {
			return errors.New("rollup.finalization.calendar.bucket is required")
		}
	default:
		return fmt.Errorf("rollup.finalization.policy %q is not supported", cfg.Policy)
	}
	return nil
}

func parseRollupRollingDuration(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("duration is required")
	}
	unit := value[len(value)-1]
	multiplier := int64(1)
	switch unit {
	case 'd':
	case 'w':
		multiplier = 7
	default:
		return 0, fmt.Errorf("duration %q must use whole-day or whole-week units", value)
	}
	amount, err := strconv.ParseInt(value[:len(value)-1], 10, 64)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("duration %q must be a positive integer followed by d or w", value)
	}
	if amount > (1<<63-1)/multiplier {
		return 0, fmt.Errorf("duration %q is too large", value)
	}
	return amount * multiplier, nil
}

func LoadConfiguration(path string, options ...func(*gencfg.ProcessingOptions)) (*Config, error) {
	haveFile := len(path) > 0
	options = append(requiredProcessingOptions(), options...)

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
	return gencfg.Process(ConfigTmpl, requiredProcessingOptions()...)
}

func Dump(cfg *Config) ([]byte, error) {
	dumpCfg := *cfg
	dumpCfg.INPX.CommentTemplate = commentTemplatePlaceholder
	dumpCfg.INPX.VersionTemplate = versionTemplatePlaceholder

	data, err := yaml.Marshal(dumpCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config to yaml: %w", err)
	}
	out := string(data)
	if out, err = replaceDumpPlaceholder(out, commentTemplateFieldName, commentTemplatePlaceholder, cfg.INPX.CommentTemplate); err != nil {
		return nil, err
	}
	if out, err = replaceDumpPlaceholder(out, versionTemplateFieldName, versionTemplatePlaceholder, cfg.INPX.VersionTemplate); err != nil {
		return nil, err
	}
	return []byte(out), nil
}

func replaceDumpPlaceholder(data string, field string, placeholder string, value string) (string, error) {
	needle := field + ": " + placeholder
	replacement := field + ": " + readableYAMLQuotedString(value)
	if count := strings.Count(data, needle); count != 1 {
		return "", fmt.Errorf("failed to find %s placeholder in dumped config", field)
	}
	return strings.Replace(data, needle, replacement, 1), nil
}

func readableYAMLQuotedString(value string) string {
	return strings.ReplaceAll(strconv.Quote(value), `\ufeff`, `\uFEFF`)
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

func requiredProcessingOptions() []func(*gencfg.ProcessingOptions) {
	return append(
		defaultProcessingOptions(),
		gencfg.WithDoNotExpandField(commentTemplateFieldName),
		gencfg.WithDoNotExpandField(versionTemplateFieldName),
	)
}
