package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// memStore is an in-memory ObjectStore, so that a whole synchronization can be
// exercised with no bucket, no credentials and no network.
type memStore struct {
	mutex   sync.Mutex
	objects map[string]memObject

	// opaqueETags makes the store hand back ETags which say nothing about the
	// content, as a bucket encrypted with SSE-KMS or holding multipart uploads
	// does. The content metadata then becomes the only way to compare sides.
	opaqueETags bool
	// failPutsAfter makes the store refuse uploads once it has accepted this
	// many, standing in for a transfer which dies halfway through.
	failPutsAfter int
	puts          int
}

type memObject struct {
	content  []byte
	etag     string
	metadata map[string]string
	modified time.Time
}

func newMemStore() *memStore {
	return &memStore{objects: map[string]memObject{}}
}

func (s *memStore) List(ctx context.Context, prefix string) ([]Object, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	var objects []Object
	for key, object := range s.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		objects = append(objects, Object{
			Key:          key,
			Size:         int64(len(object.content)),
			ETag:         object.etag,
			LastModified: object.modified,
		})
	}
	// A real listing is ordered by key, and a test should not depend on Go's
	// map iteration either way.
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	return objects, nil
}

func (s *memStore) Get(ctx context.Context, key string) (*Reader, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	object, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("memStore: no such object %s", key)
	}
	metadata := map[string]string{}
	for k, v := range object.metadata {
		metadata[k] = v
	}
	return &Reader{Body: io.NopCloser(strings.NewReader(string(object.content))), Metadata: metadata}, nil
}

func (s *memStore) Head(ctx context.Context, key string) (map[string]string, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	object, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("memStore: no such object %s", key)
	}
	metadata := map[string]string{}
	for k, v := range object.metadata {
		metadata[k] = v
	}
	return metadata, nil
}

func (s *memStore) Put(ctx context.Context, key string, body io.Reader, size int64, metadata map[string]string) (string, error) {
	content, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.failPutsAfter > 0 && s.puts >= s.failPutsAfter {
		return "", fmt.Errorf("memStore: refusing to upload %s", key)
	}
	s.puts++

	sum := md5.Sum(content)
	etag := hex.EncodeToString(sum[:])
	if s.opaqueETags {
		// Still changes with the content, as a real ETag does, but is not a
		// plain MD5 and so says nothing about what the object holds.
		etag += "-1"
	}

	stored := map[string]string{}
	for k, v := range metadata {
		stored[strings.ToLower(k)] = v
	}

	s.objects[key] = memObject{content: content, etag: etag, metadata: stored, modified: time.Now()}
	return etag, nil
}

func (s *memStore) Delete(ctx context.Context, keys []string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	for _, key := range keys {
		delete(s.objects, key)
	}
	return nil
}

// keys returns the stored keys, sorted.
func (s *memStore) keys() []string {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// content returns what the store holds for a key.
func (s *memStore) content(key string) string {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return string(s.objects[key].content)
}
