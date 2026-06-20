package util

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ScanResult 包含匹配行及其行号
type ScanResult struct {
	LineNum int    `json:"line_num"`
	Content string `json:"content"`
}

// ScanLogFile 高性能流式搜索，返回匹配行及其行号。
// maxMatches 控制返回条数上限；truncated 表示是否因达到上限而被截断。
func ScanLogFile(filePath string, keyword string, maxMatches int) (results []ScanResult, truncated bool, err error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	currentLine := 0

	for scanner.Scan() {
		currentLine++
		line := scanner.Text()
		if strings.Contains(strings.ToLower(line), strings.ToLower(keyword)) {
			if len(results) < maxMatches {
				results = append(results, ScanResult{
					LineNum: currentLine,
					Content: line,
				})
			} else {
				truncated = true
				break
			}
		}
	}

	return results, truncated, scanner.Err()
}

// GetLogContext 获取特定行号前后的上下文，并标注行号
func GetLogContext(filePath string, targetLine int, before, after int) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	currentLine := 0
	start := targetLine - before
	if start < 1 {
		start = 1
	}
	end := targetLine + after

	for scanner.Scan() {
		currentLine++
		if currentLine >= start && currentLine <= end {
			prefix := "  "
			if currentLine == targetLine {
				prefix = "> " // 标记目标行
			}
			lines = append(lines, fmt.Sprintf("%s[%d] %s", prefix, currentLine, scanner.Text()))
		}
		if currentLine > end {
			break
		}
	}

	if len(lines) == 0 {
		return "未找到相关行号的内容", nil
	}

	return strings.Join(lines, "\n"), scanner.Err()
}
