package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MRKups/automain/internal/domain"
)

const (
	DefaultDBPath              = "./data/automain.db"
	DefaultLLMProvider         = "openrouter"
	DefaultLLMModel            = "google/gemini-2.5-flash"
	DefaultGeminiModel         = "gemini-2.5-flash"
	DefaultOpenAIModel         = "gpt-4o-mini"
	DefaultDeepSeekModel       = "deepseek-chat"
	DefaultGeminiBaseURL       = "https://generativelanguage.googleapis.com/v1beta"
	DefaultOpenRouterBaseURL   = "https://openrouter.ai/api/v1"
	DefaultOpenAIBaseURL       = "https://api.openai.com/v1"
	DefaultDeepSeekBaseURL     = "https://api.deepseek.com"
	DefaultFinder              = "llm"
	DefaultListenAddr          = "127.0.0.1:8080"
	DefaultTimeout             = 60 * time.Second
	EmbeddingProvider          = "openrouter"
	EmbeddingModel             = "google/gemini-embedding-2"
	DefaultEmbeddingURL        = "https://openrouter.ai/api/v1"
	DefaultEmbeddingDimensions = 3072
)

// Config holds validated runtime settings resolved from environment variables and CLI flags.
type Config struct {
	DBPath                 string
	IndexDBPath            string
	VectorDBPath           string
	QdrantHost             string
	QdrantPort             int
	QdrantTLS              bool
	QdrantAPIKey           string
	QdrantCollectionPrefix string
	qdrantTLSInvalid       bool
	EmbeddingAPIKey        string
	EmbeddingModel         string
	EmbeddingBaseURL       string
	EmbeddingDimensions    int
	DecisionAPIKey         string
	DecisionModel          string
	DecisionEndpoint       string
	Repository             string
	GitHubToken            string
	LLMProvider            string
	LLMAPIKey              string
	LLMModel               string
	LLMBaseURL             string
	Finder                 string
	ListenAddr             string
	Timeout                time.Duration
	JSONOutput             bool
}

// NormalizeLLMProvider resolves the default provider and direct-provider aliases.
func NormalizeLLMProvider(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return DefaultLLMProvider
	case "openrouter":
		return "openrouter"
	case "gemini", "google":
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
	embeddingModel := firstNonEmptyEnv("AUTOMAIN_EMBEDDING_MODEL")
	if embeddingModel == "" {
		embeddingModel = EmbeddingModel
	}
	embeddingDimensions := DefaultEmbeddingDimensions
	if raw := strings.TrimSpace(os.Getenv("AUTOMAIN_EMBEDDING_DIMENSIONS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 128 || parsed > 3072 {
			embeddingDimensions = -1 // Preserve malformed environment input for validation.
		} else {
			embeddingDimensions = parsed
		}
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

	qdrantHost, qdrantPort, qdrantTLS, qdrantTLSInvalid, qdrantPrefix := qdrantFromEnv()
	return Config{
		DBPath:                 dbPath,
		IndexDBPath:            strings.TrimSpace(os.Getenv("AUTOMAIN_INDEX_DB_PATH")),
		VectorDBPath:           strings.TrimSpace(os.Getenv("AUTOMAIN_VECTOR_DB_PATH")),
		QdrantHost:             qdrantHost,
		QdrantPort:             qdrantPort,
		QdrantTLS:              qdrantTLS,
		QdrantAPIKey:           strings.TrimSpace(os.Getenv("AUTOMAIN_QDRANT_API_KEY")),
		QdrantCollectionPrefix: qdrantPrefix,
		qdrantTLSInvalid:       qdrantTLSInvalid,
		EmbeddingAPIKey:        firstNonEmptyEnv("AUTOMAIN_EMBEDDING_API_KEY", "OPENROUTER_API_KEY"),
		EmbeddingModel:         embeddingModel,
		EmbeddingBaseURL:       firstNonEmptyEnv("AUTOMAIN_EMBEDDING_BASE_URL"),
		EmbeddingDimensions:    embeddingDimensions,
		DecisionAPIKey:         firstNonEmptyEnv("AUTOMAIN_DECISION_API_KEY", "OPENROUTER_API_KEY"),
		DecisionModel:          firstNonEmptyEnv("AUTOMAIN_DECISION_MODEL"),
		DecisionEndpoint:       firstNonEmptyEnv("AUTOMAIN_DECISION_ENDPOINT"),
		Repository:             strings.TrimSpace(os.Getenv("AUTOMAIN_REPO")),
		GitHubToken:            strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		LLMProvider:            provider,
		LLMAPIKey:              llmKey,
		LLMModel:               llmModel,
		LLMBaseURL:             llmBaseURL,
		Finder:                 finder,
		ListenAddr:             listenAddr,
		Timeout:                DefaultTimeout,
	}
}

func resolveLLMProviderSettings(provider string) (apiKey, model, baseURL string) {
	apiKey = strings.TrimSpace(os.Getenv("AUTOMAIN_LLM_API_KEY"))
	model = strings.TrimSpace(os.Getenv("AUTOMAIN_LLM_MODEL"))
	baseURL = firstNonEmptyEnv("AUTOMAIN_LLM_API_URL", "AUTOMAIN_LLM_BASE_URL")

	switch provider {
	case "openrouter":
		if apiKey == "" {
			apiKey = firstNonEmptyEnv("OPENROUTER_API_KEY")
		}
		if model == "" {
			model = firstNonEmptyEnv("OPENROUTER_MODEL")
		}
		if model == "" {
			model = DefaultLLMModel
		}
		if baseURL == "" {
			baseURL = firstNonEmptyEnv("OPENROUTER_API_URL", "OPENROUTER_BASE_URL")
		}
		if baseURL == "" {
			baseURL = DefaultOpenRouterBaseURL
		}
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
			model = DefaultGeminiModel
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
	if err := c.ValidateDatabasePaths(); err != nil {
		return domain.NewError(domain.ErrorCodeInvalidInput, operation, err.Error(), "Use separate files for --db, --index-db, and the legacy --vector-db source.", err)
	}
	if err := c.ValidateQdrantSettings(); err != nil {
		return domain.NewError(domain.ErrorCodeInvalidInput, operation, err.Error(), "Check AUTOMAIN_QDRANT_HOST, AUTOMAIN_QDRANT_PORT, AUTOMAIN_QDRANT_TLS, and AUTOMAIN_QDRANT_COLLECTION_PREFIX.", err)
	}
	if err := c.ValidateEmbeddingSettings(); err != nil {
		return domain.NewError(domain.ErrorCodeInvalidInput, operation, err.Error(), "Check AUTOMAIN_EMBEDDING_MODEL, AUTOMAIN_EMBEDDING_BASE_URL, and AUTOMAIN_EMBEDDING_DIMENSIONS.", err)
	}
	if err := c.ValidateDecisionSettings(); err != nil {
		return domain.NewError(domain.ErrorCodeInvalidInput, operation, err.Error(), "Check AUTOMAIN_DECISION_MODEL and AUTOMAIN_DECISION_ENDPOINT.", err)
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
	case "openrouter", "gemini", "openai", "deepseek":
	default:
		return domain.NewError(
			domain.ErrorCodeInvalidInput,
			operation,
			fmt.Sprintf("Unsupported LLM provider %q.", c.LLMProvider),
			"Set AUTOMAIN_LLM_PROVIDER to openrouter, gemini, openai, or deepseek.",
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

// IndexPath defaults next to the selected source database.
func (c Config) IndexPath() string {
	if strings.TrimSpace(c.IndexDBPath) != "" {
		return c.IndexDBPath
	}
	return filepath.Join(filepath.Dir(c.DBPath), "index.db")
}

// VectorPath is the legacy migration source only. Production must not open it.
func (c Config) VectorPath() string {
	if strings.TrimSpace(c.VectorDBPath) != "" {
		return c.VectorDBPath
	}
	return filepath.Join(filepath.Dir(c.DBPath), "vectors.db")
}

func (c Config) EmbeddingURL() string {
	if base := strings.TrimRight(strings.TrimSpace(c.EmbeddingBaseURL), "/"); base != "" {
		return base
	}
	return DefaultEmbeddingURL
}

func (c Config) EmbeddingSize() int {
	if c.EmbeddingDimensions == 0 {
		return DefaultEmbeddingDimensions
	}
	return c.EmbeddingDimensions
}

func (c Config) EmbeddingAPIKeySet() bool {
	return strings.TrimSpace(c.EmbeddingAPIKey) != ""
}

// Local HTTP overrides support offline checks. Keys must not be put in the URL,
// and remote endpoints require HTTPS. This setting is independent of LLM config.
func (c Config) ValidateEmbeddingSettings() error {
	if model := strings.TrimSpace(c.EmbeddingModel); model != "" && model != EmbeddingModel {
		return fmt.Errorf("embedding model must be %s; no alternative model is supported", EmbeddingModel)
	}
	if size := c.EmbeddingSize(); size < 128 || size > 3072 {
		return fmt.Errorf("Gemini Embedding 2 dimensions must be between 128 and 3072")
	}
	u, err := url.Parse(c.EmbeddingURL())
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("embedding base URL must be an absolute URL without credentials, query parameters, or a fragment")
	}
	local := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		local = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return fmt.Errorf("embedding base URL must use HTTPS, except for local fixture servers")
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
