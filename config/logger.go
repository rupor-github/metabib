package config

import (
	"errors"
	"fmt"
	"io"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zapio"
)

type LoggerConfig struct {
	Level       string `yaml:"level" validate:"required,oneof=none debug normal"`
	Destination string `yaml:"destination,omitempty" sanitize:"path_clean,assure_dir_exists_for_file" validate:"omitempty,filepath"`
	Mode        string `yaml:"mode,omitempty" validate:"omitempty,oneof=append overwrite"`
}

type LoggingConfig struct {
	ConsoleLogger LoggerConfig `yaml:"console"`
	FileLogger    LoggerConfig `yaml:"file"`
}

func (conf *LoggingConfig) Prepare(appName string) (*zap.Logger, io.WriteCloser, error) {
	consoleLPConfig := zap.NewDevelopmentEncoderConfig()
	consoleLPConfig.EncodeCaller = nil
	if EnableColorOutput(os.Stdout) {
		consoleLPConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		consoleLPConfig.TimeKey = zapcore.OmitKey
	} else {
		consoleLPConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	}
	consoleEncoder := zapcore.NewConsoleEncoder(consoleLPConfig)

	consoleHPConfig := zap.NewDevelopmentEncoderConfig()
	consoleHPConfig.EncodeCaller = nil
	if EnableColorOutput(os.Stderr) {
		consoleHPConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		consoleHPConfig.TimeKey = zapcore.OmitKey
	} else {
		consoleHPConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	}
	highPriority := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool { return lvl >= zapcore.ErrorLevel })

	var consoleCoreHP, consoleCoreLP zapcore.Core
	switch conf.ConsoleLogger.Level {
	case "normal":
		consoleCoreLP = zapcore.NewCore(consoleEncoder, zapcore.Lock(os.Stdout), zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return zapcore.InfoLevel <= lvl && lvl < zapcore.ErrorLevel
		}))
		consoleCoreHP = zapcore.NewCore(newConsoleEncoder(consoleHPConfig), zapcore.Lock(os.Stderr), highPriority)
	case "debug":
		consoleCoreLP = zapcore.NewCore(consoleEncoder, zapcore.Lock(os.Stdout), zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return zapcore.DebugLevel <= lvl && lvl < zapcore.ErrorLevel
		}))
		consoleCoreHP = zapcore.NewCore(newConsoleEncoder(consoleHPConfig), zapcore.Lock(os.Stderr), highPriority)
	default:
		consoleCoreLP = zapcore.NewNopCore()
		consoleCoreHP = zapcore.NewNopCore()
	}

	fileCore := zapcore.NewNopCore()
	var file *os.File
	var processLog io.WriteCloser = nopWriteCloser{io.Discard}
	var err error
	if conf.FileLogger.Level != "none" {
		level := zap.NewAtomicLevelAt(zap.InfoLevel)
		if conf.FileLogger.Level == "debug" {
			level = zap.NewAtomicLevelAt(zap.DebugLevel)
		}
		file, err = openLogFile(conf.FileLogger.Destination, conf.FileLogger.Mode)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to access file log destination %q: %w", conf.FileLogger.Destination, err)
		}
		fileCore = zapcore.NewCore(zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()), zapcore.Lock(file), level)
		processLogger := zap.New(fileCore).Named(appName).Named("mariadb")
		processLog = &combinedWriteCloser{
			Writer: &zapio.Writer{Log: processLogger, Level: zapcore.InfoLevel},
			closers: []io.Closer{
				file,
			},
		}
	}

	logger := zap.New(zapcore.NewTee(consoleCoreHP, consoleCoreLP, fileCore), zap.AddCaller()).Named(appName)
	return logger, processLog, nil
}

type nopWriteCloser struct {
	io.Writer
}

func (n nopWriteCloser) Close() error {
	return nil
}

type combinedWriteCloser struct {
	io.Writer
	closers []io.Closer
}

func (c *combinedWriteCloser) Close() error {
	var errs []error
	for _, closer := range c.closers {
		if err := closer.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func openLogFile(path string, mode string) (*os.File, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if mode == "append" {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0o644)
}

type consoleEnc struct {
	zapcore.Encoder
}

func newConsoleEncoder(cfg zapcore.EncoderConfig) zapcore.Encoder {
	return consoleEnc{zapcore.NewConsoleEncoder(cfg)}
}

func (c consoleEnc) Clone() zapcore.Encoder {
	return consoleEnc{c.Encoder.Clone()}
}

func (c consoleEnc) EncodeEntry(ent zapcore.Entry, fields []zapcore.Field) (*buffer.Buffer, error) {
	var newFields []zapcore.Field
	for _, f := range fields {
		if f.Type == zapcore.ErrorType {
			e := f.Interface.(error)
			f.Interface = errors.New(e.Error())
		}
		newFields = append(newFields, f)
	}
	return c.Encoder.EncodeEntry(ent, newFields)
}
