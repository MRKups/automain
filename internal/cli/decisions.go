package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MRKups/automain/internal/config"
	"github.com/MRKups/automain/internal/domain"
)

func runDecide(ctx context.Context, factory ServiceFactory, cfg config.Config, args []string, jsonMode, helpRequested bool, stdout, stderr io.Writer) int {
	if helpRequested {
		fmt.Fprintln(stdout, "Usage: automain decide --input <request.json> [--timeout <duration>] [--json]")
		fmt.Fprintln(stdout, "Send state and typed questions (noul|choice|score) to Jev through OpenRouter.")
		fmt.Fprintln(stdout, "Returns typed answers and probabilities; no generated text or relationship writes.")
		fmt.Fprintln(stdout, "Input budget: 30000 UTF-8 JSON bytes including model; oversized input is rejected, not truncated.")
		return 0
	}
	fs := newSubFlagSet("decide")
	inputPath := fs.String("input", "", "JSON file containing state and questions (no model or credentials)")
	if err := parseSubcommandFlags(fs, "decide", args, "Run 'automain decide --help'."); err != nil {
		return writeError(stdout, stderr, jsonMode, "decide", err)
	}
	if strings.TrimSpace(*inputPath) == "" {
		return writeError(stdout, stderr, jsonMode, "decide", decisionFileError("A decision request file is required."))
	}
	data, err := readDecisionFile(*inputPath)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "decide", err)
	}
	input, err := domain.DecodeDecisionRequest(data)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "decide", err)
	}
	svc, cleanup, err := factory(ctx, cfg)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "decide", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	result, err := svc.Decide(ctx, input)
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "decide", err)
	}
	if jsonMode {
		err = writeJSONSuccess(stdout, "decide", result)
	} else {
		// Even the human surface preserves probabilities rather than inventing prose.
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(result)
	}
	if err != nil {
		return writeError(stdout, stderr, jsonMode, "decide", err)
	}
	return 0
}

func readDecisionFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, decisionFileError("Cannot open the decision request file.")
	}
	defer func() {
		if err := file.Close(); err != nil {
			return
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, decisionFileError("Decision input must be a regular JSON file.")
	}
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxDecisionRequestBytes+1))
	if err != nil {
		return nil, decisionFileError("Cannot read the decision request file.")
	}
	return data, nil
}

func decisionFileError(message string) error {
	return domain.NewError(domain.ErrorCodeInvalidInput, "decide", message, "Use --input <request.json>; see internal/decision/README.md for the format.", nil)
}
