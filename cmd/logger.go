package cmd

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func newLogger(level, format string) (*zap.Logger, error) {
	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("log level %q: %w", level, err)
	}

	cfg := zap.NewProductionConfig()
	switch format {
	case "json":
		// Field names Cloud Logging understands, so levels show up in Cloud Run.
		cfg.EncoderConfig.LevelKey = "severity"
		cfg.EncoderConfig.MessageKey = "message"
		cfg.EncoderConfig.EncodeLevel = cloudLoggingLevel
	case "console":
		cfg = zap.NewDevelopmentConfig()
	default:
		return nil, fmt.Errorf("unknown log format %q", format)
	}

	cfg.Level = zap.NewAtomicLevelAt(lvl)
	cfg.DisableStacktrace = true
	cfg.EncoderConfig.TimeKey = "time"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	return cfg.Build()
}

// Cloud Logging severities, see LogSeverity in its API.
var severities = map[zapcore.Level]string{
	zapcore.DebugLevel:  "DEBUG",
	zapcore.InfoLevel:   "INFO",
	zapcore.WarnLevel:   "WARNING",
	zapcore.ErrorLevel:  "ERROR",
	zapcore.DPanicLevel: "CRITICAL",
	zapcore.PanicLevel:  "CRITICAL",
	zapcore.FatalLevel:  "ALERT",
}

func cloudLoggingLevel(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	severity, ok := severities[level]
	if !ok {
		severity = "DEFAULT"
	}
	enc.AppendString(severity)
}
