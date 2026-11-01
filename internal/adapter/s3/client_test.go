package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zk-org/zk/internal/core"
	"github.com/zk-org/zk/internal/util/opt"
	"github.com/zk-org/zk/internal/util/test/assert"
)

// fakeS3 is enough of the S3 HTTP API to drive the real AWS SDK through
// Client.
//
// The in-memory ObjectStore used by the synchronization tests stops short of
// the SDK, so nothing else exercises the request signing, the pagination, the
// metadata headers or the ETag quoting. Those are where a wrong assumption
// about the protocol would otherwise go unnoticed until it met a real bucket.
type fakeS3 struct {
	t *testing.T
	// mutex guards the maps: the synchronization makes several requests at
	// once, and net/http serves each in its own goroutine.
	mutex   sync.Mutex
	objects map[string][]byte
	meta    map[string]map[string]string
	// pageSize makes the listing truncate, so that the paginator is exercised
	// rather than taken on trust.
	pageSize int
}

func newFakeS3(t *testing.T) *fakeS3 {
	return &fakeS3{t: t, objects: map[string][]byte{}, meta: map[string]map[string]string{}, pageSize: 1000}
}

func etagOf(content []byte) string {
	sum := md5.Sum(content)
	return hex.EncodeToString(sum[:])
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Path-style addressing, which is what the client is configured for here:
	// /<bucket>/<key...>
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		f.list(w, r)
	case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
		f.delete(w, r)
	case r.Method == http.MethodPut:
		f.put(w, r, key)
	case r.Method == http.MethodGet:
		f.get(w, key, true)
	case r.Method == http.MethodHead:
		f.get(w, key, false)
	default:
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}
}

func (f *fakeS3) list(w http.ResponseWriter, r *http.Request) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	prefix := r.URL.Query().Get("prefix")

	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	// Continue from where the last page stopped, as a real listing does.
	if after := r.URL.Query().Get("continuation-token"); after != "" {
		for len(keys) > 0 && keys[0] <= after {
			keys = keys[1:]
		}
	}

	truncated := false
	if len(keys) > f.pageSize {
		keys = keys[:f.pageSize]
		truncated = true
	}

	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
	fmt.Fprintf(&body, "<IsTruncated>%t</IsTruncated>", truncated)
	if truncated {
		fmt.Fprintf(&body, "<NextContinuationToken>%s</NextContinuationToken>", keys[len(keys)-1])
	}
	for _, key := range keys {
		content := f.objects[key]
		fmt.Fprintf(&body,
			`<Contents><Key>%s</Key><Size>%d</Size><ETag>&quot;%s&quot;</ETag><LastModified>%s</LastModified></Contents>`,
			key, len(content), etagOf(content), time.Unix(1700000000, 0).UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	body.WriteString(`</ListBucketResult>`)

	w.Header().Set("Content-Type", "application/xml")
	io.WriteString(w, body.String())
}

func (f *fakeS3) put(w http.ResponseWriter, r *http.Request, key string) {
	content, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// A custom endpoint must not be sent the checksums the SDK adds by
	// default, as several S3-compatible services reject them. Without
	// RequestChecksumCalculationWhenRequired an x-amz-checksum-crc32 header
	// arrives here, so this is what keeps that setting honest.
	for name := range r.Header {
		lowered := strings.ToLower(name)
		if strings.HasPrefix(lowered, "x-amz-checksum-") || strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
			f.t.Errorf("unexpected %s uploading %s: a custom endpoint may reject it", lowered, key)
		}
	}

	metadata := map[string]string{}
	for name, values := range r.Header {
		if lowered := strings.ToLower(name); strings.HasPrefix(lowered, "x-amz-meta-") {
			metadata[strings.TrimPrefix(lowered, "x-amz-meta-")] = values[0]
		}
	}

	f.mutex.Lock()
	f.objects[key] = content
	f.meta[key] = metadata
	f.mutex.Unlock()

	// Quoted, as S3 returns it, so that the trimming is exercised.
	w.Header().Set("ETag", `"`+etagOf(content)+`"`)
	w.WriteHeader(http.StatusOK)
}

func (f *fakeS3) get(w http.ResponseWriter, key string, body bool) {
	f.mutex.Lock()
	content, ok := f.objects[key]
	metadata := map[string]string{}
	for name, value := range f.meta[key] {
		metadata[name] = value
	}
	f.mutex.Unlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	for name, value := range metadata {
		w.Header().Set("x-amz-meta-"+name, value)
	}
	w.Header().Set("ETag", `"`+etagOf(content)+`"`)
	w.Header().Set("Content-Length", fmt.Sprint(len(content)))
	w.WriteHeader(http.StatusOK)
	if body {
		w.Write(content)
	}
}

func (f *fakeS3) delete(w http.ResponseWriter, r *http.Request) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	var request struct {
		Objects []struct {
			Key string `xml:"Key"`
		} `xml:"Object"`
	}
	content, _ := io.ReadAll(r.Body)
	if err := xml.Unmarshal(content, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	for _, object := range request.Objects {
		delete(f.objects, object.Key)
		delete(f.meta, object.Key)
	}

	w.Header().Set("Content-Type", "application/xml")
	io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><DeleteResult></DeleteResult>`)
}

// newTestClient points a real Client at the fake server.
func newTestClient(t *testing.T, fake *fakeS3) *Client {
	t.Helper()

	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	// Static credentials, so that the credential chain neither reads the
	// developer's own configuration nor reaches for instance metadata.
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	client, err := NewClient(context.Background(), core.S3Config{
		Bucket:      opt.NewString("notes"),
		EndpointURL: opt.NewString(server.URL),
		PathStyle:   true,
	})
	assert.Nil(t, err)
	return client
}

// TestClientRoundTrip drives the real SDK through every call the
// synchronization makes.
func TestClientRoundTrip(t *testing.T) {
	fake := newFakeS3(t)
	client := newTestClient(t, fake)
	ctx := context.Background()

	content := "# Hello"
	metadata := map[string]string{
		MetaMD5:   etagOf([]byte(content)),
		MetaMTime: "2024-05-14T09:32:11Z",
	}

	etag, err := client.Put(ctx, "notebook/hello.md", strings.NewReader(content), int64(len(content)), metadata)
	assert.Nil(t, err)
	// Returned unquoted, which is what the state records and compares.
	assert.Equal(t, etag, etagOf([]byte(content)))

	objects, err := client.List(ctx, "notebook/")
	assert.Nil(t, err)
	assert.Equal(t, len(objects), 1)
	assert.Equal(t, objects[0].Key, "notebook/hello.md")
	assert.Equal(t, objects[0].Size, int64(len(content)))
	assert.Equal(t, objects[0].ETag, etagOf([]byte(content)))
	assert.True(t, IsMD5(objects[0].ETag))

	reader, err := client.Get(ctx, "notebook/hello.md")
	assert.Nil(t, err)
	downloaded, err := io.ReadAll(reader.Body)
	assert.Nil(t, err)
	reader.Body.Close()
	assert.Equal(t, string(downloaded), content)
	// The metadata has to survive the round trip, as the modification time and
	// the content hash both travel in it.
	assert.Equal(t, reader.Metadata[MetaMTime], "2024-05-14T09:32:11Z")
	assert.Equal(t, reader.Metadata[MetaMD5], etagOf([]byte(content)))

	head, err := client.Head(ctx, "notebook/hello.md")
	assert.Nil(t, err)
	assert.Equal(t, head[MetaMD5], etagOf([]byte(content)))

	assert.Nil(t, client.Delete(ctx, []string{"notebook/hello.md"}))
	objects, err = client.List(ctx, "notebook/")
	assert.Nil(t, err)
	assert.Equal(t, len(objects), 0)
}

// TestClientListPaginates checks that a notebook larger than one page is
// listed in full, which a bucket of any size depends on.
func TestClientListPaginates(t *testing.T) {
	fake := newFakeS3(t)
	fake.pageSize = 2
	client := newTestClient(t, fake)
	ctx := context.Background()

	for i := range 7 {
		key := fmt.Sprintf("notebook/note-%02d.md", i)
		fake.objects[key] = []byte(fmt.Sprintf("note %d", i))
	}

	objects, err := client.List(ctx, "notebook/")
	assert.Nil(t, err)
	assert.Equal(t, len(objects), 7)
	assert.Equal(t, objects[0].Key, "notebook/note-00.md")
	assert.Equal(t, objects[6].Key, "notebook/note-06.md")
}

// TestClientListHonoursThePrefix checks that one bucket can hold several
// notebooks without a listing picking up the neighbours.
func TestClientListHonoursThePrefix(t *testing.T) {
	fake := newFakeS3(t)
	client := newTestClient(t, fake)

	fake.objects["notebook/mine.md"] = []byte("mine")
	fake.objects["other/theirs.md"] = []byte("theirs")

	objects, err := client.List(context.Background(), "notebook/")
	assert.Nil(t, err)
	assert.Equal(t, len(objects), 1)
	assert.Equal(t, objects[0].Key, "notebook/mine.md")
}

// TestClientGetMissing checks that a missing object is an error rather than an
// empty note written over a real one.
func TestClientGetMissing(t *testing.T) {
	client := newTestClient(t, newFakeS3(t))

	_, err := client.Get(context.Background(), "notebook/absent.md")
	assert.Err(t, err, "failed to download")
}

// TestBackendOverTheSDK runs a whole synchronization through the real client
// and the fake server, rather than against the in-memory store: it is the
// closest thing to a real bucket that needs no credentials.
func TestBackendOverTheSDK(t *testing.T) {
	fake := newFakeS3(t)
	// Small enough that the notebook spans several pages.
	fake.pageSize = 2
	client := newTestClient(t, fake)

	dir := t.TempDir()
	backend := NewBackend(BackendOpts{
		NotebookPath: dir,
		Store:        client,
		Bucket:       "notes",
		Prefix:       "notebook",
		MaxDeletes:   10,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})

	for i := range 5 {
		writeFile(t, dir, fmt.Sprintf("note-%d.md", i), fmt.Sprintf("# Note %d", i))
	}
	assert.Nil(t, backend.Sync(core.SyncOpts{Quiet: true}))

	keys := make([]string, 0, len(fake.objects))
	for key := range fake.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assert.Equal(t, keys, []string{
		"notebook/note-0.md", "notebook/note-1.md", "notebook/note-2.md",
		"notebook/note-3.md", "notebook/note-4.md",
	})

	// A second machine takes them all, across the page boundaries.
	other := t.TempDir()
	otherBackend := NewBackend(BackendOpts{
		NotebookPath: other,
		Store:        client,
		Bucket:       "notes",
		Prefix:       "notebook",
		MaxDeletes:   10,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	assert.Nil(t, otherBackend.Sync(core.SyncOpts{Quiet: true}))

	for i := range 5 {
		name := fmt.Sprintf("note-%d.md", i)
		assert.Equal(t, readFile(t, other, name), fmt.Sprintf("# Note %d", i))
	}
}
