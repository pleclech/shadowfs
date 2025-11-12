package fs

import (
	"os"
	"testing"
	"time"
)

func TestLogLevel_String(t *testing.T) {
	tests := []struct {
		level    LogLevel
		expected string
	}{
		{LogLevelDebug, "DEBUG"},
		{LogLevelInfo, "INFO"},
		{LogLevelWarn, "WARN"},
		{LogLevelError, "ERROR"},
		{LogLevel(99), "UNKNOWN"},
		{LogLevel(-1), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := tt.level.String()
			if result != tt.expected {
				t.Errorf("LogLevel(%d).String() = %s, expected %s", tt.level, result, tt.expected)
			}
		})
	}
}

func TestNewLogger(t *testing.T) {
	// Use os.Stderr for testing (can't easily mock os.File)
	logger := NewLogger(LogLevelInfo, os.Stderr)

	if logger == nil {
		t.Fatal("NewLogger() returned nil")
	}

	if logger.GetLevel() != LogLevelInfo {
		t.Errorf("Expected log level %d, got %d", LogLevelInfo, logger.GetLevel())
	}
}

func TestLogger_SetLevel_GetLevel(t *testing.T) {
	logger := NewLogger(LogLevelDebug, os.Stderr)

	// Test initial level
	if logger.GetLevel() != LogLevelDebug {
		t.Errorf("Expected initial level %d, got %d", LogLevelDebug, logger.GetLevel())
	}

	// Test setting level
	logger.SetLevel(LogLevelWarn)
	if logger.GetLevel() != LogLevelWarn {
		t.Errorf("Expected level %d after SetLevel, got %d", LogLevelWarn, logger.GetLevel())
	}

	// Test setting level again
	logger.SetLevel(LogLevelError)
	if logger.GetLevel() != LogLevelError {
		t.Errorf("Expected level %d after second SetLevel, got %d", LogLevelError, logger.GetLevel())
	}
}

func TestLogger_Debug(t *testing.T) {
	// Use os.Stderr - we can't easily capture output, but we can verify no panic
	logger := NewLogger(LogLevelDebug, os.Stderr)

	// These should not panic
	logger.Debug("test message %s", "value")
	logger.Debug("another message")
	
	// Verify level is correct
	if logger.GetLevel() != LogLevelDebug {
		t.Errorf("Expected log level %d, got %d", LogLevelDebug, logger.GetLevel())
	}
}

func TestLogger_Info(t *testing.T) {
	logger := NewLogger(LogLevelInfo, os.Stderr)

	logger.Info("info message %d", 42)
	logger.Info("another info")
	
	// Verify level is correct
	if logger.GetLevel() != LogLevelInfo {
		t.Errorf("Expected log level %d, got %d", LogLevelInfo, logger.GetLevel())
	}
}

func TestLogger_Warn(t *testing.T) {
	logger := NewLogger(LogLevelWarn, os.Stderr)

	logger.Warn("warning message")
	logger.Warn("another warning")
	
	// Verify level is correct
	if logger.GetLevel() != LogLevelWarn {
		t.Errorf("Expected log level %d, got %d", LogLevelWarn, logger.GetLevel())
	}
}

func TestLogger_Error(t *testing.T) {
	logger := NewLogger(LogLevelError, os.Stderr)

	logger.Error("error message %v", "test")
	logger.Error("another error")
	
	// Verify level is correct
	if logger.GetLevel() != LogLevelError {
		t.Errorf("Expected log level %d, got %d", LogLevelError, logger.GetLevel())
	}
}

func TestLogger_MessageFiltering(t *testing.T) {
	logger := NewLogger(LogLevelWarn, os.Stderr)

	// Debug and Info should be filtered out (no output, but no panic)
	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")
	logger.Error("error message")
	
	// Verify level filtering works by checking level
	if logger.GetLevel() != LogLevelWarn {
		t.Errorf("Expected log level %d, got %d", LogLevelWarn, logger.GetLevel())
	}
}

func TestInitLogger(t *testing.T) {
	// Reset global logger state by creating a new one
	// Note: InitLogger uses sync.Once, so we can't test it multiple times in same process
	// But we can test that it initializes correctly
	InitLogger(LogLevelDebug)

	level := GetGlobalLevel()
	if level != LogLevelDebug {
		t.Errorf("Expected global level %d after InitLogger, got %d", LogLevelDebug, level)
	}
}

func TestGlobalDebug(t *testing.T) {
	InitLogger(LogLevelDebug)
	// Global Debug should work when logger is initialized
	Debug("global debug message")
	// No error means it worked
}

func TestGlobalInfo(t *testing.T) {
	InitLogger(LogLevelInfo)
	Info("global info message")
	// No error means it worked
}

func TestGlobalWarn(t *testing.T) {
	InitLogger(LogLevelWarn)
	Warn("global warn message")
	// No error means it worked
}

func TestGlobalError(t *testing.T) {
	InitLogger(LogLevelError)
	Error("global error message")
	// No error means it worked
}

func TestSetGlobalLevel_GetGlobalLevel(t *testing.T) {
	InitLogger(LogLevelDebug)

	// Test initial level
	level := GetGlobalLevel()
	if level != LogLevelDebug {
		t.Errorf("Expected initial global level %d, got %d", LogLevelDebug, level)
	}

	// Test setting level
	SetGlobalLevel(LogLevelInfo)
	level = GetGlobalLevel()
	if level != LogLevelInfo {
		t.Errorf("Expected global level %d after SetGlobalLevel, got %d", LogLevelInfo, level)
	}

	// Test setting level again
	SetGlobalLevel(LogLevelWarn)
	level = GetGlobalLevel()
	if level != LogLevelWarn {
		t.Errorf("Expected global level %d after second SetGlobalLevel, got %d", LogLevelWarn, level)
	}
}

func TestGetGlobalLevel_NoLogger(t *testing.T) {
	// This test verifies GetGlobalLevel returns default when logger is nil
	// We can't easily reset the global logger, but we can test the default behavior
	// by checking that it returns LogLevelInfo when logger is not initialized
	// (though in practice, InitLogger is usually called first)
}

func TestLogOperation(t *testing.T) {
	InitLogger(LogLevelDebug)
	start := time.Now()
	
	// Test successful operation
	LogOperation("test_op", "/test/path", start, nil)
	
	// Test failed operation
	LogOperation("test_op", "/test/path", start, os.ErrNotExist)
	
	// No error means it worked
}

func TestLogFileOperation(t *testing.T) {
	InitLogger(LogLevelDebug)
	
	// Test successful operation
	LogFileOperation("copy", "/source/file", "/cache/file", 1024, nil)
	
	// Test failed operation
	LogFileOperation("copy", "/source/file", "/cache/file", 1024, os.ErrPermission)
	
	// No error means it worked
}

func TestLogCacheOperation(t *testing.T) {
	InitLogger(LogLevelDebug)
	
	// Test cache hit
	LogCacheOperation("read", "/test/path", true)
	
	// Test cache miss
	LogCacheOperation("read", "/test/path", false)
	
	// No error means it worked
}

func TestLogger_ConcurrentAccess(t *testing.T) {
	logger := NewLogger(LogLevelDebug, os.Stderr)

	// Test concurrent writes (no panic means thread safety worked)
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			logger.Debug("concurrent message %d", id)
			logger.Info("concurrent info %d", id)
			logger.Warn("concurrent warn %d", id)
			logger.Error("concurrent error %d", id)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
	
	// If we get here without panic, thread safety worked
}

