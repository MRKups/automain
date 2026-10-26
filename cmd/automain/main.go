package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/MRKups/automain/internal/analysis"
	"github.com/MRKups/automain/internal/app"
	"github.com/MRKups/automain/internal/cli"
	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/decision"
	"github.com/MRKups/automain/internal/embedding"
	"github.com/MRKups/automain/internal/github"
	"github.com/MRKups/automain/internal/storage/catalog"
	"github.com/MRKups/automain/internal/storage/qdrant"
	"github.com/MRKups/automain/internal/storage/sqlite"
	"github.com/MRKups/automain/internal/storage/vector"
	"github.com/MRKups/automain/web"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	baseCfg := config.FromEnv()
	factory := func(cmdCtx context.Context, cfg config.Config) (*app.Service, func(), error) {
		store, err := sqlite.Open(cmdCtx, cfg.DBPath)
		if err != nil {
			return nil, nil, err
		}
		// Check again after opening the source file, so previously nonexistent
		// paths through symlinked parent directories cannot alias the same DB.
		if err := cfg.ValidateDatabasePaths(); err != nil {
			if closeErr := store.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "error closing sqlite database: %v\n", closeErr)
			}
			return nil, nil, err
		}
		index, err := catalog.Open(cmdCtx, cfg.IndexPath())
		if err != nil {
			if closeErr := store.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "error closing sqlite database: %v\n", closeErr)
			}
			return nil, nil, err
		}
		if err := cfg.ValidateDatabasePaths(); err != nil {
			if closeErr := index.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "error closing catalog database: %v\n", closeErr)
			}
			if closeErr := store.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "error closing sqlite database: %v\n", closeErr)
			}
			return nil, nil, err
		}
		vectors, err := qdrant.New(qdrant.Options{
			Host: cfg.QdrantHost, Port: cfg.QdrantPort, TLS: cfg.QdrantTLS,
			APIKey: cfg.QdrantAPIKey, CollectionPrefix: cfg.QdrantCollectionPrefix,
		})
		if err != nil {
			if closeErr := index.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "error closing catalog database: %v\n", closeErr)
			}
			if closeErr := store.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "error closing sqlite database: %v\n", closeErr)
			}
			return nil, nil, err
		}
		cleanup := func() {
			if closeErr := vectors.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "error closing Qdrant client: %v\n", closeErr)
			}
			if closeErr := index.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "error closing catalog database: %v\n", closeErr)
			}
			if closeErr := store.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "error closing sqlite database: %v\n", closeErr)
			}
		}
		llmClient := analysis.NewClient(cfg)
		finder, err := analysis.NewFinder(cfg.Finder, llmClient)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		svc := app.NewService(cfg, app.Adapters{
			Store:      store,
			GitHub:     github.NewClient(cfg.GitHubToken),
			Finder:     finder,
			Analyser:   llmClient,
			Catalog:    index,
			Vectors:    vectors,
			Embedder:   embedding.NewOpenRouter(cfg),
			Decider:    decision.NewOpenRouter(cfg),
			OpenLegacy: vector.OpenLegacy,
			EmbeddedUI: web.HasIndex(),
		})
		return svc, cleanup, nil
	}

	code := cli.Execute(ctx, os.Args[1:], baseCfg, factory, os.Stdout, os.Stderr)
	os.Exit(code)
}
