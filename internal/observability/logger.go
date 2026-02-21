package observability

import (
	"encoding/json"
	"io"
	"log"
	"time"
)

const (
	levelInfo  = "info"
	levelWarn  = "warn"
	levelError = "error"
)

// Field is a single structured log field.
type Field struct {
	Key   string
	Value any
}

// Any builds a structured field from an arbitrary value.
func Any(key string, value any) Field {
	return Field{Key: key, Value: value}
}

// String builds a structured field for string values.
func String(key, value string) Field {
	return Field{Key: key, Value: value}
}

// Int builds a structured field for int values.
func Int(key string, value int) Field {
	return Field{Key: key, Value: value}
}

// Float64 builds a structured field for float values.
func Float64(key string, value float64) Field {
	return Field{Key: key, Value: value}
}

// Logger writes JSON logs with timestamp, level, message, and fields.
type Logger struct {
	base *log.Logger
}

// NewLogger constructs a structured logger that writes to the provided sink.
func NewLogger(w io.Writer) *Logger {
	if w == nil {
		w = io.Discard
	}

	return &Logger{
		base: log.New(w, "", 0),
	}
}

// Info writes an info-level log line.
func (l *Logger) Info(message string, fields ...Field) {
	l.log(levelInfo, message, fields...)
}

// Warn writes a warn-level log line.
func (l *Logger) Warn(message string, fields ...Field) {
	l.log(levelWarn, message, fields...)
}

// Error writes an error-level log line.
func (l *Logger) Error(message string, fields ...Field) {
	l.log(levelError, message, fields...)
}

func (l *Logger) log(level, message string, fields ...Field) {
	if l == nil || l.base == nil {
		return
	}

	record := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"level": level,
		"msg":   message,
	}

	for _, field := range fields {
		if field.Key == "" {
			continue
		}
		record[field.Key] = field.Value
	}

	encoded, err := json.Marshal(record)
	if err != nil {
		l.base.Printf(`{"ts":%q,"level":"%s","msg":"log encoding error","error":%q}`, record["ts"], level, err.Error())
		return
	}

	l.base.Println(string(encoded))
}
