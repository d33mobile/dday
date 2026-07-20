// Policy gates around reacting to a "!start": which rooms the bot listens in,
// the per-handle rate limit and the injectable clock they rely on.

package matrixbot

import (
	"strings"
	"time"
)

// defaultRateWindow is the minimum interval between two "!start" commands
// from the same handle that the bot will act on. Further commands inside the
// window are ignored (logged), so a user spamming "!start" gets a single DM.
const defaultRateWindow = 30 * time.Second

// ParseAllowedRooms parses a comma-separated room-id list (the ALLOWED_ROOMS
// env var) into a set. Blank entries are skipped; an empty result means "react
// everywhere".
func ParseAllowedRooms(s string) map[string]bool {
	m := make(map[string]bool)
	for _, part := range strings.Split(s, ",") {
		if r := strings.TrimSpace(part); r != "" {
			m[r] = true
		}
	}
	return m
}

// clock returns the current time, honouring the injectable c.now for tests.
func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// roomAllowed reports whether Run should react to "!start" in roomID. With
// no allowlist configured every room is allowed.
func (c *Client) roomAllowed(roomID string) bool {
	if len(c.AllowedRooms) == 0 {
		return true
	}
	return c.AllowedRooms[roomID]
}

// rateLimited reports whether a "!start" from handle arrives too soon after
// the last one the bot acted on. On the accepted path it records the time so
// the next command starts a fresh window. Safe for concurrent use.
func (c *Client) rateLimited(handle string) bool {
	window := c.RateWindow
	if window <= 0 {
		window = defaultRateWindow
	}
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastSeen == nil {
		c.lastSeen = make(map[string]time.Time)
	}
	if last, ok := c.lastSeen[handle]; ok && now.Sub(last) < window {
		return true
	}
	c.lastSeen[handle] = now
	return false
}
