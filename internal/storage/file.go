package storage

import (
	"bufio"
	"fmt"
	"os"

	"masss/internal/domain"
)

// FileStorage implements domain.Storage for file-based persistence
type FileStorage struct{}

// NewFileStorage creates a new file storage
func NewFileStorage() *FileStorage {
	return &FileStorage{}
}

// Save writes proxies to a file
func (fs *FileStorage) Save(proxies []domain.Proxy, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for _, proxy := range proxies {
		if _, err := writer.WriteString(string(proxy) + "\n"); err != nil {
			return fmt.Errorf("failed to write to file: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush writer: %w", err)
	}

	return nil
}

// Load reads proxies from a file
func (fs *FileStorage) Load(filename string) ([]domain.Proxy, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var proxies []domain.Proxy
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			proxies = append(proxies, domain.Proxy(line))
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return proxies, nil
}
