package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/domain"
)

const (
	DefaultDBPath     = "./data/automain.db"
	DefaultLLMModel   = "gemini-2.5-flash"
	DefaultFinder     = "llm"
	DefaultListenAddr = "127.0.0.1:8080"
	DefaultTimeout    = 60 * time.Second
)

// Config holds validated runtime settings resolved from environment variables and CLI flags.
type Config struct {
	DBPath      string
	Repository  string
	GitHubToken string
	LLMAPIKey   string
	LLMModel    string
	Finder      string
	ListenAddr  string
	Timeout     time.Duration
	JSONOutput  bool
}

// FromEnv constructs a baseline Config from process environment variables.
func FromEnv() Config {
	dbPath := strings.TrimSpace(os.Getenv("AUTOMAIN_DB_PATH"))
	if dbPath == "" {
		dbPath = DefaultDBPath
	}

	llmKey := strings.TrimSpace(os.Getenv("AUTOMAIN_LLM_API_KEY"))
	if llmKey == "" {
		llmKey = strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	}

	llmModel := strings.TrimSpace(os.Getenv("AUTOMAIN_LLM_MODEL"))
	if llmModel == "" {
		llmModel = strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	}
	if llmModel == "" {
		llmModel = DefaultLLMModel
	}

	finder := strings.TrimSpace(os.Getenv("AUTOMAIN_FINDER"))
	if finder == "" {
		finder = DefaultFinder
	}

	listenAddr := strings.TrimSpace(os.Getenv("AUTOMAIN_LISTEN_ADDR"))
	if listenAddr == "" {
		if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
			listenAddr = "0.0.0.0:" + port
		} else {
			listenAddr = DefaultListenAddr
		}
	}

	return Config{
		DBPath:      dbPath,
		Repository:  strings.TrimSpace(os.Getenv("AUTOMAIN_REPO")),
		GitHubToken: strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		LLMAPIKey:   llmKey,
		LLMModel:    llmModel,
		Finder:      finder,
		ListenAddr:  listenAddr,
		Timeout:     DefaultTimeout,
	}
}

// Validate checks configuration invariants and returns a structured domain.Error on failure.
func (c Config) Validate(operation string) error {
	if strings.TrimSpace(c.DBPath) == "" {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			"Database path (--db or AUTOMAIN_DB_PATH) must not be empty.",
			"Pass a valid SQLite file path via --db <path> or set AUTOMAIN_DB_PATH.",
			nil,
		)
	}
	if strings.TrimSpace(c.ListenAddr) == "" {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			"Listen address (--listen or AUTOMAIN_LISTEN_ADDR) must not be empty.",
			"Pass --listen 127.0.0.1:8080 or set AUTOMAIN_LISTEN_ADDR.",
			nil,
		)
	}
	if c.Timeout <= 0 {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			fmt.Sprintf("Invalid timeout %s; timeout must be greater than 0s.", c.Timeout),
			"Pass a positive duration such as --timeout 60s.",
			nil,
		)
	}
	if strings.TrimSpace(c.Repository) != "" {
		if err := domain.ValidateRepoSlug(c.Repository); err != nil {
			return domain.NewError(
				domain.ErrorCodeInvalidInput,
				operation,
				err.Error(),
				"Pass the repository in owner/name format, for example --repo cli/cli.",
				err,
			)
		}
	}
	switch c.Finder {
	case "llm", "fts", "vector":
	default:
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			fmt.Sprintf("Unsupported candidate finder strategy %q.", c.Finder),
			"Choose one of: --finder llm, --finder fts, or --finder vector.",
			nil,
		)
	}
	return nil
}

// GitHubTokenSet reports whether a GitHub token is present without exposing the secret.
func (c Config) GitHubTokenSet() bool {
	return c.GitHubToken != ""
}

// LLMAPIKeySet reports whether an LLM API key is present without exposing the secret.
func (c Config) LLMAPIKeySet() bool {
	return c.LLMAPIKey != ""
}
