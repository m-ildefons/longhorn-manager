package cache

type Cache interface {
	Put(key string, value any)
	Get(key string) any
	Key(kind, name, info string) string
}
