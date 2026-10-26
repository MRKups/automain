package config

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	DefaultQdrantHost             = "127.0.0.1"
	DefaultQdrantPort             = 6334
	DefaultQdrantCollectionPrefix = "automain_"
)

var (
	qdrantDNSLabel = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	qdrantPrefix   = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
)

func qdrantFromEnv() (host string, port int, tls, invalidTLS bool, prefix string) {
	host = firstNonEmptyEnv("AUTOMAIN_QDRANT_HOST")
	if host == "" {
		host = DefaultQdrantHost
	}
	port = DefaultQdrantPort
	if raw := strings.TrimSpace(os.Getenv("AUTOMAIN_QDRANT_PORT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			port = -1
		} else {
			port = parsed
		}
	}
	if raw := strings.TrimSpace(os.Getenv("AUTOMAIN_QDRANT_TLS")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		invalidTLS = err != nil
		tls = parsed
	}
	prefix = firstNonEmptyEnv("AUTOMAIN_QDRANT_COLLECTION_PREFIX")
	if prefix == "" {
		prefix = DefaultQdrantCollectionPrefix
	}
	return
}

// ValidateQdrantSettings performs local checks only, without credentials or DNS probes.
func (c Config) ValidateQdrantSettings() error {
	if c.qdrantTLSInvalid {
		return fmt.Errorf("Qdrant TLS setting must be true or false")
	}
	if c.QdrantPort < 1 || c.QdrantPort > 65535 {
		return fmt.Errorf("Qdrant gRPC port must be between 1 and 65535")
	}
	host := c.QdrantHost
	if host == "" || host != strings.TrimSpace(host) || len(host) > 253 {
		return fmt.Errorf("Qdrant host must be a hostname or IP address without a URL, port, or credentials")
	}
	private := false
	if ip := net.ParseIP(host); ip != nil {
		private = ip.IsLoopback() || ip.IsPrivate()
	} else {
		for _, label := range strings.Split(host, ".") {
			if !qdrantDNSLabel.MatchString(label) {
				return fmt.Errorf("Qdrant host must be a hostname or IP address without a URL, port, or credentials")
			}
		}
		private = !strings.Contains(host, ".")
	}
	if !private && !c.QdrantTLS {
		return fmt.Errorf("Qdrant remote or public hosts require TLS; set AUTOMAIN_QDRANT_TLS=true")
	}
	if !qdrantPrefix.MatchString(c.QdrantCollectionPrefix) {
		return fmt.Errorf("Qdrant collection prefix must contain 1-128 ASCII letters, digits, underscores, or hyphens")
	}
	return nil
}

func (c Config) QdrantAPIKeySet() bool {
	return strings.TrimSpace(c.QdrantAPIKey) != ""
}
