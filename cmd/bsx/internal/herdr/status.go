package herdr

import (
	"context"
	"fmt"
	"strings"
)

// Status is herdr status's text for c's server as section.key → value (server.status,
// server.version, client.version, update.restart_needed, ...). A server that isn't
// running is a status, not an error: the error is herdr status not answering.
func (c Client) Status(ctx context.Context) (map[string]string, error) {
	argv := c.Argv("status")
	out, err := c.Run.Run(ctx, "", argv...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	m, section := map[string]string{}, ""
	for _, l := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), ":")
		switch {
		case !ok:
		case !strings.HasPrefix(l, " "):
			section = k
		default:
			m[section+"."+k] = strings.TrimSpace(v)
		}
	}
	return m, nil
}
