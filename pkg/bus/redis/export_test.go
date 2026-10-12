package redis

// SetBeforeXRead makes every read of b call f between its last context check
// and its XREAD.
func SetBeforeXRead(b *Bus, f func()) { b.beforeXRead = f }
