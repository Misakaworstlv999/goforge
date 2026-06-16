package builtin

import (
	"context"
	"fmt"
	"time"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// ClockArgs defines the input for the clock tool.
type ClockArgs struct {
	Timezone string `json:"timezone,omitempty" jsonschema:"description=IANA timezone name (e.g. America/New_York). Defaults to UTC."`
}

// NewClock returns a tool that reports the current date and time.
func NewClock() tool.Tool {
	return tool.NewTool[ClockArgs]("current_time", "Get current date and time", clockFn)
}

func clockFn(_ context.Context, args ClockArgs) (string, error) {
	loc := time.UTC
	if args.Timezone != "" {
		var err error
		loc, err = time.LoadLocation(args.Timezone)
		if err != nil {
			return "", fmt.Errorf("invalid timezone %q: %w", args.Timezone, err)
		}
	}
	now := time.Now().In(loc)
	return now.Format(time.RFC3339), nil
}
