package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Replay protection (issue #34). A delivery is processed once, whichever
// replica receives it and across restarts: replay keys are claimed in the
// database (store.MarkSeen), and only kept in memory without one.
//
// Keys:
//   - a signed GitLab delivery's webhook-id, for the signature window;
//   - a hash of every verified body, for replayTTL. GitHub and Bitbucket
//     signatures carry no timestamp and their delivery ID headers aren't
//     signed, and a GitLab secret token is the same on every delivery, so
//     a captured request could otherwise be replayed later. Real events
//     differ (IDs, times, commits), so an identical body is a replay or a
//     retry of one already received. It also merges one GitLab event
//     arriving through several hooks.

// replayTTL is how long a verified body is remembered.
const replayTTL = 7 * 24 * time.Hour

// firstDelivery claims key for ttl and reports whether it was new. When
// the database can't be reached it falls back to this console's memory.
func (s *Server) firstDelivery(ctx context.Context, key string, ttl time.Duration) bool {
	if s.store != nil {
		first, err := s.store.MarkSeen(ctx, key, ttl)
		if err == nil {
			return first
		}
		s.logger.Warn("could not record a webhook replay key in the database; using this console's memory", "error", err)
	}
	return s.replays.first(key, ttl, time.Now())
}

// bodyKey is the replay key of a verified body.
func bodyKey(platform, host string, body []byte) string {
	sum := sha256.Sum256(body)
	return platform + "|" + host + "|body|" + hex.EncodeToString(sum[:])
}
