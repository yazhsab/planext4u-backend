package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	fileSecretSuffix = "_FILE_VALUE"
	secretDirectory  = "/run/planext4u-secrets"
)

func main() {
	if err := run(os.Args, os.Environ(), os.Setenv, os.Unsetenv, os.MkdirAll, os.WriteFile, os.Chown, syscall.Setgroups, syscall.Setgid, syscall.Setuid, syscall.Exec); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "secret bootstrap failed")
		os.Exit(126)
	}
}

type setenvFunc func(string, string) error
type unsetenvFunc func(string) error
type mkdirFunc func(string, os.FileMode) error
type writeFileFunc func(string, []byte, os.FileMode) error
type chownFunc func(string, int, int) error
type setgroupsFunc func([]int) error
type setIDFunc func(int) error
type execFunc func(string, []string, []string) error

func run(args, environment []string, setenv setenvFunc, unsetenv unsetenvFunc, mkdir mkdirFunc, writeFile writeFileFunc, chown chownFunc, setgroups setgroupsFunc, setgid, setuid setIDFunc, execute execFunc) error {
	if len(args) < 2 || !filepath.IsAbs(args[1]) || setenv == nil || unsetenv == nil || mkdir == nil || writeFile == nil || chown == nil || setgroups == nil || setgid == nil || setuid == nil || execute == nil {
		return errors.New("invalid bootstrap invocation")
	}
	secrets := fileSecrets(environment)
	if len(secrets) > 0 {
		if err := mkdir(secretDirectory, 0o700); err != nil {
			return err
		}
		if err := chown(secretDirectory, 65532, 65532); err != nil {
			return err
		}
	}
	for source, value := range secrets {
		target := strings.TrimSuffix(source, "_VALUE")
		digest := sha256.Sum256([]byte(target))
		path := filepath.Join(secretDirectory, strings.ToLower(hex.EncodeToString(digest[:8])))
		if err := writeFile(path, []byte(value), 0o600); err != nil {
			return err
		}
		if err := chown(path, 65532, 65532); err != nil {
			return err
		}
		if err := setenv(target, path); err != nil {
			return err
		}
		if err := unsetenv(source); err != nil {
			return err
		}
	}
	if err := setgroups([]int{}); err != nil {
		return err
	}
	if err := setgid(65532); err != nil {
		return err
	}
	if err := setuid(65532); err != nil {
		return err
	}
	return execute(args[1], args[1:], os.Environ())
}

func fileSecrets(environment []string) map[string]string {
	result := map[string]string{}
	for _, item := range environment {
		key, value, found := strings.Cut(item, "=")
		if !found || !strings.HasSuffix(key, fileSecretSuffix) || !safeEnvironmentName(key) || value == "" {
			continue
		}
		result[key] = value
	}
	return result
}

func safeEnvironmentName(value string) bool {
	if value == "" || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
