package redis

import "github.com/bsamanvith/agentkit/pkg/bus"

// SetBeforeXRead makes every read of b call f between its last context check
// and its XREAD.
func SetBeforeXRead(b *Bus, f func()) { b.beforeXRead = f }

// Permanent is permanent, for its table test.
var Permanent = permanent

// Forget makes b forget what it published to t, as when t's stream expires.
func Forget(b *Bus, t bus.Topic) {
	b.published.mu.Lock()
	defer b.published.mu.Unlock()
	delete(b.published.ids, b.Key(t))
}
