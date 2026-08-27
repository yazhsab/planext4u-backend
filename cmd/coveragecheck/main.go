package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	profile := flag.String("profile", "", "Go coverage profile")
	minimum := flag.Float64("min", 0, "minimum statement coverage percentage")
	flag.Parse()
	coverage, err := profileCoverage(*profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("statement coverage %.1f%% (minimum %.1f%%)\n", coverage, *minimum)
	if coverage+0.000001 < *minimum {
		os.Exit(1)
	}
}

func profileCoverage(path string) (float64, error) {
	if strings.TrimSpace(path) == "" {
		return 0, fmt.Errorf("coverage profile is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open coverage profile: %w", err)
	}
	defer file.Close()
	var total, covered int64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "mode:") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return 0, fmt.Errorf("invalid coverage profile line")
		}
		statements, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || statements < 0 {
			return 0, fmt.Errorf("invalid statement count")
		}
		count, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || count < 0 {
			return 0, fmt.Errorf("invalid coverage count")
		}
		total += statements
		if count > 0 {
			covered += statements
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read coverage profile: %w", err)
	}
	if total == 0 {
		return 0, fmt.Errorf("coverage profile contains no statements")
	}
	return float64(covered) * 100 / float64(total), nil
}
