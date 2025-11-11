package fs

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// LogLevel represents the severity of a log message
type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarn
	LogLevelError
)

// String returns the string representation of the log level
func (l LogLevel) String() string {
	switch l {
	case LogLevelDebug:
		return "DEBUG"
	case LogLevelInfo:
		return "INFO"
	case LogLevelWarn:
		return "WARN"
	case LogLevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Logger provides structured logging for the filesystem
type Logger struct {
	level  LogLevel
	logger *log.Logger
	mu     sync.RWMutex
}

var (
	// Global logger instance
	globalLogger *Logger
	loggerOnce   sync.Once
)

// InitLogger initializes the global logger
func InitLogger(level LogLevel) {
	loggerOnce.Do(func() {
		globalLogger = NewLogger(level, os.Stderr)
	})
}

// NewLogger creates a new Logger instance
func NewLogger(level LogLevel, output *os.File) *Logger {
	return &Logger{
		level:  level,
		logger: log.New(output, "", 0), // No default prefix, we'll add our own
	}
}

// SetLevel sets the minimum log level
func (l *Logger) SetLevel(level LogLevel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// GetLevel returns the current log level
func (l *Logger) GetLevel() LogLevel {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.level
}

// Debug logs a debug message
func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(LogLevelDebug, format, args...)
}

// Info logs an info message
func (l *Logger) Info(format string, args ...interface{}) {
	l.log(LogLevelInfo, format, args...)
}

// Warn logs a warning message
func (l *Logger) Warn(format string, args ...interface{}) {
	l.log(LogLevelWarn, format, args...)
}

// Error logs an error message
func (l *Logger) Error(format string, args ...interface{}) {
	l.log(LogLevelError, format, args...)
}

// log is the internal logging method
func (l *Logger) log(level LogLevel, format string, args ...interface{}) {
	l.mu.RLock()
	if level < l.level {
		l.mu.RUnlock()
		return
	}
	l.mu.RUnlock()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	message := fmt.Sprintf(format, args...)

	l.logger.Printf("[%s] %s: %s", timestamp, level.String(), message)
}

// Global logging functions that use the global logger

// Debug logs a debug message to the global logger
func Debug(format string, args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Debug(format, args...)
	}
}

// Info logs an info message to the global logger
func Info(format string, args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Info(format, args...)
	}
}

// Warn logs a warning message to the global logger
func Warn(format string, args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Warn(format, args...)
	}
}

// Error logs an error message to the global logger
func Error(format string, args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Error(format, args...)
	}
}

// SetGlobalLevel sets the log level for the global logger
func SetGlobalLevel(level LogLevel) {
	if globalLogger != nil {
		globalLogger.SetLevel(level)
	}
}

// GetGlobalLevel returns the current global log level
func GetGlobalLevel() LogLevel {
	if globalLogger != nil {
		return globalLogger.GetLevel()
	}
	return LogLevelInfo
}

// LogOperation logs a filesystem operation with timing
func LogOperation(operation, path string, start time.Time, err error) {
	duration := time.Since(start)
	if err != nil {
		Error("Operation failed: %s on %s (duration: %v) - %v", operation, path, duration, err)
	} else {
		Debug("Operation completed: %s on %s (duration: %v)", operation, path, duration)
	}
}

// LogFileOperation logs a file operation with details
func LogFileOperation(operation, sourcePath, cachePath string, size int64, err error) {
	if err != nil {
		Error("File operation failed: %s (source: %s, cache: %s, size: %d) - %v",
			operation, sourcePath, cachePath, size, err)
	} else {
		Debug("File operation completed: %s (source: %s, cache: %s, size: %d)",
			operation, sourcePath, cachePath, size)
	}
}

// LogCacheOperation logs cache-related operations
func LogCacheOperation(operation, path string, hit bool) {
	if hit {
		Debug("Cache hit: %s for %s", operation, path)
	} else {
		Debug("Cache miss: %s for %s", operation, path)
	}
}
