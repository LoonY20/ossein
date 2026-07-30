package migrate

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"
)

// HandleCommand executes a migration command from application arguments.
//
// Supported commands are migrate, migrate:rollback, and migrate:status.
// It returns handled=false for arguments that belong to another application
// command.
func HandleCommand(
	ctx context.Context,
	args []string,
	runner *Runner,
	migrations []Migration,
	output io.Writer,
) (handled bool, err error) {
	if len(args) == 0 || !IsCommand(args[0]) {
		return false, nil
	}
	if runner == nil {
		return true, errors.New("ossein migrate: runner cannot be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if output == nil {
		output = io.Discard
	}

	switch args[0] {
	case "migrate":
		limit, err := parseIntFlag(args[0], args[1:], "limit", 0)
		if err != nil {
			return true, err
		}
		count, err := runner.Up(ctx, migrations, limit)
		if err != nil {
			return true, err
		}
		_, err = fmt.Fprintf(output, "Applied %d migration(s).\n", count)
		return true, err
	case "migrate:rollback":
		steps, err := parseIntFlag(args[0], args[1:], "steps", 1)
		if err != nil {
			return true, err
		}
		count, err := runner.Down(ctx, migrations, steps)
		if err != nil {
			return true, err
		}
		_, err = fmt.Fprintf(output, "Rolled back %d migration(s).\n", count)
		return true, err
	case "migrate:status":
		if len(args) != 1 {
			return true, fmt.Errorf("ossein migrate: %s does not accept arguments", args[0])
		}
		statuses, err := runner.Statuses(ctx, migrations)
		if err != nil {
			return true, err
		}
		return true, writeStatuses(output, statuses)
	default:
		return false, nil
	}
}

// IsCommand reports whether command is handled by HandleCommand.
//
// Applications can use it to avoid opening a database connection for
// unrelated application commands.
func IsCommand(command string) bool {
	switch command {
	case "migrate", "migrate:rollback", "migrate:status":
		return true
	default:
		return false
	}
}

func parseIntFlag(command string, args []string, name string, defaultValue int) (int, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	value := flags.Int(name, defaultValue, "")
	if err := flags.Parse(args); err != nil {
		return 0, fmt.Errorf("ossein migrate: %s: %w", command, err)
	}
	if flags.NArg() != 0 {
		return 0, fmt.Errorf("ossein migrate: %s: unexpected argument %q", command, flags.Arg(0))
	}
	return *value, nil
}

func writeStatuses(output io.Writer, statuses []Status) error {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "STATE\tVERSION\tNAME\tAPPLIED AT"); err != nil {
		return err
	}
	for _, status := range statuses {
		state := "pending"
		appliedAt := "-"
		if status.Applied {
			state = "applied"
			appliedAt = status.AppliedAt.UTC().Format(time.RFC3339)
		}
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\n",
			state,
			strconv.FormatInt(status.Version, 10),
			status.Name,
			appliedAt,
		); err != nil {
			return err
		}
	}
	return writer.Flush()
}
