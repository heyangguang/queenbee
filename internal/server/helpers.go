package server

import (
	"bufio"
	"os"
	"path/filepath"
)

// readFileBytes 读取文件
func readFileBytes(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// writeFileBytes 写文件
func writeFileBytes(path string, data []byte) error {
	os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, data, 0o644)
}

// readLastLines 读取文件最后 n 行
func readLastLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// readDir 读取目录条目
func readDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}
