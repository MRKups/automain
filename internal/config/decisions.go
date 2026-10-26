package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

const (
	DecisionProvider     = "openrouter"
	DefaultDecisionModel = "typesafe/jev-1.14"
	DefaultDecisionURL   = "https://openrouter.ai/api/alpha/decisions"
)

func (c Config) DecisionModelName() string {
	if model := strings.TrimSpace(c.DecisionModel); model != "" {
		return model
	}
	return DefaultDecisionModel
}

// This is a full endpoint: Decisions does not live under /api/v1.
func (c Config) DecisionURL() string {
	if endpoint := strings.TrimSpace(c.DecisionEndpoint); endpoint != "" {
		return endpoint
	}
	return DefaultDecisionURL
}

func (c Config) DecisionAPIKeySet() bool {
	return strings.TrimSpace(c.DecisionAPIKey) != ""
}

func (c Config) ValidateDecisionSettings() error {
	switch c.DecisionModelName() {
	case "typesafe/jev-1.14", "typesafe/jev-1.13", "~typesafe/jev-latest":
	default:
		return fmt.Errorf("decision model must be typesafe/jev-1.14, typesafe/jev-1.13, or ~typesafe/jev-latest; chat models are not supported")
	}
	u, err := url.Parse(c.DecisionURL())
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("decision endpoint must be an absolute URL without credentials, query parameters, or a fragment")
	}
	local := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		local = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return fmt.Errorf("decision endpoint must use HTTPS, except for local fixture servers")
	}
	if u.Path == "" || u.Path == "/" {
		return fmt.Errorf("decision endpoint must include the full request path, normally /api/alpha/decisions")
	}
	return nil
}
