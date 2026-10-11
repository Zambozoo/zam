package base

import (
	"fmt"
	"io"
	"os"
)

const (
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiReset  = "\033[0m"
)

type LogLevel string

const (
	LogLevelNone  LogLevel = "none"
	LogLevelError LogLevel = "error"
	LogLevelWarn  LogLevel = "warn"
	LogLevelInfo  LogLevel = "info"
)

type Logger struct{}

func (l *Logger) Print(level LogLevel, format string, args ...any) {
	var w io.Writer = os.Stderr
	var color string
	switch level {
	case LogLevelError:
		w = os.Stderr
		color = ansiRed
	case LogLevelWarn:
		w = os.Stdout
		color = ansiYellow
	case LogLevelInfo:
		w = os.Stdout
		color = ansiGreen
	}

	fmt.Fprintf(w, color+format+ansiReset, args...)
}

func (l *Logger) Flush() {
	os.Stdout.Sync()
	os.Stderr.Sync()
}
