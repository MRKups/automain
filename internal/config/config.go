package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/domain"
)

const (
	DefaultDBPath          = "./data/automain.db"
	DefaultLLMProvider     = "gemini"
	DefaultLLMModel        = "gemini-2.5-flash"
	DefaultOpenAIModel     = "gpt-4o-mini"
	DefaultDeepSeekModel   = "deepseek-chat"
	DefaultGeminiBaseURL   = "https://generativelanguage.googleapis.com/v1beta"
	DefaultOpenAIBaseURL   = "https://api.openai.com/v1"
	DefaultDeepSeekBaseURL = "https://api.deepseek.com"
	DefaultFinder          = "llm"
	DefaultListenAddr      = "127.0.0.1:8080"
	DefaultTimeout         = 60 * time.Second
)

// Config holds validated runtime settings resolved from environment variables and CLI flags.
type Config struct {
	DBPath      string
	Repository  string
	GitHubToken string
	LLMProvider string
	LLMAPIKey   string
	LLMModel    string
	LLMBaseURL  string
	Finder      string
	ListenAddr  string
	Timeout     time.Duration
	JSONOutput  bool
}

// NormalizeLLMProvider normalizes provider aliases to canonical names ("gemini", "openai", "deepseek").
func NormalizeLLMProvider(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "gemini", "google":
		if strings.TrimSpace(raw) == "" {
			return DefaultLLMProvider
		}
		return "gemini"
	case "openai", "openai-compatible":
		return "openai"
	case "deepseek":
		return "deepseek"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// FromEnv constructs a baseline Config from process environment variables,
// loading unset variables from a root ".env" file when present.
func FromEnv() Config {
	if err := LoadDotEnv(".env"); err != nil {
		// Proceed with process environment if .env cannot be read; validation surfaces missing required settings.
		fmt.Fprintf(os.Stderr, "warning: failed to read .env: %v\n", err)
	}

	dbPath := strings.TrimSpace(os.Getenv("AUTOMAIN_DB_PATH"))
	if dbPath == "" {
		dbPath = DefaultDBPath
	}

	provider := NormalizeLLMProvider(os.Getenv("AUTOMAIN_LLM_PROVIDER"))
	llmKey, llmModel, llmBaseURL := resolveLLMProviderSettings(provider)

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
		LLMProvider: provider,
		LLMAPIKey:   llmKey,
		LLMModel:    llmModel,
		LLMBaseURL:  llmBaseURL,
		Finder:      finder,
		ListenAddr:  listenAddr,
		Timeout:     DefaultTimeout,
	}
}

func resolveLLMProviderSettings(provider string) (apiKey, model, baseURL string) {
	apiKey = strings.TrimSpace(os.Getenv("AUTOMAIN_LLM_API_KEY"))
	model = strings.TrimSpace(os.Getenv("AUTOMAIN_LLM_MODEL"))
	baseURL = firstNonEmptyEnv("AUTOMAIN_LLM_API_URL", "AUTOMAIN_LLM_BASE_URL")

	switch provider {
	case "openai":
		if apiKey == "" {
			apiKey = firstNonEmptyEnv("OPENAI_API_KEY", "DEEPSEEK_API_KEY")
		}
		if model == "" {
			model = firstNonEmptyEnv("OPENAI_MODEL")
		}
		if model == "" {
			model = DefaultOpenAIModel
		}
		if baseURL == "" {
			baseURL = firstNonEmptyEnv("OPENAI_API_URL", "OPENAI_BASE_URL")
		}
		if baseURL == "" {
			baseURL = DefaultOpenAIBaseURL
		}
	case "deepseek":
		if apiKey == "" {
			apiKey = firstNonEmptyEnv("DEEPSEEK_API_KEY", "OPENAI_API_KEY")
		}
		if model == "" {
			model = firstNonEmptyEnv("DEEPSEEK_MODEL", "OPENAI_MODEL")
		}
		if model == "" {
			model = DefaultDeepSeekModel
		}
		if baseURL == "" {
			baseURL = firstNonEmptyEnv("DEEPSEEK_API_URL", "DEEPSEEK_BASE_URL", "OPENAI_API_URL", "OPENAI_BASE_URL")
		}
		if baseURL == "" {
			baseURL = DefaultDeepSeekBaseURL
		}
	default:
		if apiKey == "" {
			apiKey = firstNonEmptyEnv("GEMINI_API_KEY")
		}
		if model == "" {
			model = firstNonEmptyEnv("GEMINI_MODEL")
		}
		if model == "" {
			model = DefaultLLMModel
		}
		if baseURL == "" {
			baseURL = firstNonEmptyEnv("GEMINI_API_URL", "GEMINI_BASE_URL")
		}
		if baseURL == "" {
			baseURL = DefaultGeminiBaseURL
		}
	}
	return apiKey, model, baseURL
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
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
	_, portStr, err := net.SplitHostPort(strings.TrimSpace(c.ListenAddr))
	if err != nil {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			fmt.Sprintf("Invalid listen address %q: %v.", c.ListenAddr, err),
			"Pass a host:port address such as --listen 127.0.0.1:8080.",
			err,
		)
	}
	portNum, portErr := strconv.Atoi(portStr)
	if portErr != nil || portNum < 0 || portNum > 65535 {
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			fmt.Sprintf("Invalid listen port %q in %q; port must be an integer between 0 and 65535.", portStr, c.ListenAddr),
			"Pass a valid TCP port such as --listen 127.0.0.1:8080.",
			portErr,
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
	switch NormalizeLLMProvider(c.LLMProvider) {
	case "gemini", "openai", "deepseek":
	default:
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			fmt.Sprintf("Unsupported LLM provider %q.", c.LLMProvider),
			"Set AUTOMAIN_LLM_PROVIDER to gemini, openai, or deepseek.",
			nil,
		)
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
