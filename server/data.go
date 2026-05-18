package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	dataDirOnce sync.Once
	dataDirPath string
)

// RecMu protects the recordings JSON read-modify-write cycle
// to prevent concurrent uploads from corrupting data.
// Shared between channel and router packages.
var RecMu sync.Mutex

// DataDir returns the directory used for local JSON data files.
// Priority: DATA_DIR env var → ./database/ (matches Docker volume mapping).
func DataDir() string {
	dataDirOnce.Do(func() {
		dataDirPath = os.Getenv("DATA_DIR")
		if dataDirPath == "" {
			dataDirPath = "database"
		}
	})
	return dataDirPath
}

// DataPath returns the full path for a file inside the data directory.
func DataPath(filename string) string {
	return filepath.Join(DataDir(), filename)
}

// EnsureDataDir creates the data directory if it does not exist.
func EnsureDataDir() error {
	return os.MkdirAll(DataDir(), 0o755)
}

// ReadDataFile reads a file from the data directory.
// Returns nil if the file does not exist or cannot be read.
func ReadDataFile(filename string) []byte {
	data, err := os.ReadFile(DataPath(filename))
	if err != nil {
		return nil
	}
	return data
}

// WriteDataFile writes data to a file in the data directory.
// Creates the directory if it does not exist.
func WriteDataFile(filename string, data []byte) error {
	if err := EnsureDataDir(); err != nil {
		return fmt.Errorf("ensure data dir: %w", err)
	}
	return os.WriteFile(DataPath(filename), data, 0o644)
}

// ConfDir returns the directory used for configuration files.
// Priority: CONF_DIR env var → ./conf/.
func ConfDir() string {
	dir := os.Getenv("CONF_DIR")
	if dir == "" {
		dir = "conf"
	}
	return dir
}

// ConfPath returns the full path for a file inside the conf directory.
func ConfPath(filename string) string {
	return filepath.Join(ConfDir(), filename)
}

// EnsureConfDir creates the conf directory if it does not exist.
func EnsureConfDir() error {
	return os.MkdirAll(ConfDir(), 0o755)
}

// ReadConfFile reads a file from the conf directory.
// Returns nil if the file does not exist or cannot be read.
func ReadConfFile(filename string) []byte {
	data, err := os.ReadFile(ConfPath(filename))
	if err != nil {
		return nil
	}
	return data
}

// WriteConfFile writes data to a file in the conf directory.
// Creates the directory if it does not exist.
func WriteConfFile(filename string, data []byte) error {
	if err := EnsureConfDir(); err != nil {
		return fmt.Errorf("ensure conf dir: %w", err)
	}
	return os.WriteFile(ConfPath(filename), data, 0o644)
}
