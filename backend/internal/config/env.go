// Package config loads runtime settings from the environment and an optional .env file.
package config

import (
	"bufio"
	"os"
	"slices"
	"strings"
)

const maxEnvValueLength = 10000

// LoadDotenv reads KEY=VALUE lines from path into the process environment.
// Variables already set in the environment win, as with most dotenv loaders.
func LoadDotenv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		} else if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

// Environ is the process environment without the settings that are secrets (webhooks, tokens,
// passwords, API keys), for the programs Nightjar starts: LoadDotenv, and the service's
// EnvironmentFile, put every one of them in Nightjar's own environment, and a script that prints
// its environment or runs someone else's installer has no need of them.
func Environ() []string {
	var s Settings
	secret := map[string]bool{}
	for _, f := range s.Fields() {
		if f.Kind == KindSecret {
			secret[f.Key] = true
		}
	}
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")
		return secret[key]
	})
}

// Bool reads a true/false environment variable outside Settings (install-time switches), falling
// back to def when it is unset or invalid.
func Bool(key string, def bool) bool {
	v := def
	raw, _ := os.LookupEnv(key)
	if err := boolean(&v, key, def).Set(raw); err != nil {
		log.Warn("Invalid boolean, using default", "key", key, "value", raw, "default", def)
		return def
	}
	return v
}
