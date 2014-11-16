// Package logger implements a LRU file descriptor cache for concurrent ReadAt
// calls.
package lrufdcache

import (
	"os"
	"sync"

	"github.com/golang/groupcache/lru"
)

// A wrapper around *os.File which counts references
type CachedFile struct {
	file *os.File
	wg   sync.WaitGroup
}

// Tells the cache that we are done using the file, but it's up to the cache
// to decide when this file will really be closed. The error, if any, will be
// lost.
func (f *CachedFile) Close() error {
	f.wg.Done()
	return nil
}

// Read the file at the given offset.
func (f *CachedFile) ReadAt(buf []byte, at int64) (int, error) {
	return f.file.ReadAt(buf, at)
}

type FileCache struct {
	cache *lru.Cache
	mut   sync.RWMutex // Protects against races between concurrent opens
}

// Create a new cache with the number of entries to hold.
func NewCache(entries int) *FileCache {
	c := FileCache{
		cache: lru.New(entries),
	}

	c.cache.OnEvicted = func(key lru.Key, fdi interface{}) {
		// The file might not have been closed by all openers yet, therefore
		// spawn a routine which waits for that to happen and then closes the
		// file.
		go func(item *CachedFile) {
			item.wg.Wait()
			item.file.Close()
		}(fdi.(*CachedFile))
	}
	return &c
}

// Open and cache a file descriptor or use an existing cached descriptor for
// the given path.
func (c *FileCache) Open(path string) (*CachedFile, error) {
	// Evictions can only happen during c.cache.Add, and there is a potential
	// race between c.cache.Get and cfd.wg.Add where if not guarded by a mutex
	// could result in cfd getting closed before the counter is incremented if
	// a concurrent routine does a c.cache.Add
	c.mut.RLock()
	fdi, ok := c.cache.Get(path)
	if ok {
		cfd := fdi.(*CachedFile)
		cfd.wg.Add(1)
		c.mut.RUnlock()
		return cfd, nil
	}
	c.mut.RUnlock()

	fd, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	cfd := &CachedFile{
		file: fd,
		wg:   sync.WaitGroup{},
	}
	cfd.wg.Add(1)
	c.mut.Lock()
	c.cache.Add(path, cfd)
	c.mut.Unlock()
	return cfd, nil
}
