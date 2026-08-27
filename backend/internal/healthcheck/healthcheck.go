// Package healthcheck implements the non-network container liveness command.
package healthcheck

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

var ErrReadyRequiresHTTP = errors.New("ready health is available only from GET /api/v1/health/ready on the running service")

// Execute validates and executes a local healthcheck command. It intentionally
// does not read configuration, open storage, contact PostgreSQL, or inspect
// Worker/task state, so container liveness remains independent of readiness.
func Execute(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	kind := flags.String("kind", "", "healthcheck kind")
	configPath := flags.String("config", "", "deployment config path")
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("parse healthcheck arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("healthcheck does not accept positional arguments")
	}
	if strings.TrimSpace(*configPath) == "" {
		return errors.New("healthcheck requires --config")
	}
	switch *kind {
	case "live":
		return json.NewEncoder(output).Encode(map[string]string{
			"kind":    "live",
			"service": "web-api",
			"status":  "alive",
		})
	case "ready":
		return ErrReadyRequiresHTTP
	default:
		return fmt.Errorf("unsupported healthcheck kind %q; use live", *kind)
	}
}
