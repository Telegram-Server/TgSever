package appupdates

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"telesrv/internal/updatecdn"
)

// fakeSource 提供内存里的安装包；fail 不为 nil 时打开失败。
type fakeSource struct {
	mu    sync.Mutex
	files map[string][]byte
	fail  error
	opens atomic.Int64
	// gate 不为 nil 时 Open 等它关闭再返回，用来制造并发注册。
	gate chan struct{}
}

func (s *fakeSource) AppFiles(context.Context) ([]updatecdn.ResolvedFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []updatecdn.ResolvedFile
	for name, data := range s.files {
		out = append(out, resolvedFile(name, data))
	}
	return out, nil
}

func (s *fakeSource) OpenAppFile(_ context.Context, file updatecdn.ResolvedFile) (io.ReadCloser, error) {
	s.opens.Add(1)
	if s.gate != nil {
		<-s.gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	data, ok := s.files[file.Name]
	if !ok {
		return nil, updatecdn.ErrAppFileNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func resolvedFile(name string, data []byte) updatecdn.ResolvedFile {
	sum := sha256.Sum256(data)
	return updatecdn.ResolvedFile{Name: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), MimeType: updatecdn.AndroidPackageMimeType}
}

// fakeDocuments 把读到的字节存成内存里的文档。
type fakeDocuments struct {
	mu      sync.Mutex
	docs    map[int64]domain.Document
	bytes   map[int64][]byte
	next    int64
	creates atomic.Int64
}

func newFakeDocuments() *fakeDocuments {
	return &fakeDocuments{docs: map[int64]domain.Document{}, bytes: map[int64][]byte{}, next: 100}
}

func (d *fakeDocuments) CreateDocumentFromReader(_ context.Context, r io.Reader, spec domain.DocumentSpec, wantSize int64) (domain.Document, error) {
	d.creates.Add(1)
	data, err := io.ReadAll(r)
	if err != nil {
		return domain.Document{}, err
	}
	if int64(len(data)) != wantSize {
		return domain.Document{}, domain.ErrDocumentInvalid
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	doc := domain.Document{ID: d.next, AccessHash: 7, MimeType: spec.MimeType, Size: int64(len(data)), Attributes: spec.Attributes}
	d.docs[doc.ID] = doc
	d.bytes[doc.ID] = data
	return doc, nil
}

func (d *fakeDocuments) GetDocument(_ context.Context, id int64) (domain.Document, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	doc, ok := d.docs[id]
	return doc, ok, nil
}

// verifying 模拟 updatecdn 的来源：字节与记录不符时读到末尾报错。
type verifyingSource struct{ *fakeSource }

func (s verifyingSource) OpenAppFile(ctx context.Context, file updatecdn.ResolvedFile) (io.ReadCloser, error) {
	rc, err := s.fakeSource.OpenAppFile(ctx, file)
	if err != nil {
		return nil, err
	}
	data, _ := io.ReadAll(rc)
	if got := resolvedFile(file.Name, data); got.SHA256 != file.SHA256 {
		return io.NopCloser(io.MultiReader(bytes.NewReader(data), errReader{updatecdn.ErrAppFileMismatch})), nil
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestRegistryDocumentRegistersInBackgroundOnce(t *testing.T) {
	apk := []byte("PK\x03\x04 fake apk bytes")
	source := &fakeSource{files: map[string][]byte{"android-a-1.apk": apk}}
	docs := newFakeDocuments()
	store := memory.NewAppUpdateDocumentStore()
	registry := NewRegistry(source, store, docs, nil)
	file := resolvedFile("android-a-1.apk", apk)
	ctx := context.Background()

	if _, ready, err := registry.Document(ctx, file); err != nil || ready {
		t.Fatalf("first lookup ready=%v err=%v, want not ready", ready, err)
	}
	registry.Wait()
	doc, ready, err := registry.Document(ctx, file)
	if err != nil || !ready {
		t.Fatalf("after registration ready=%v err=%v", ready, err)
	}
	if doc.MimeType != updatecdn.AndroidPackageMimeType || doc.Size != int64(len(apk)) ||
		len(doc.Attributes) != 1 || doc.Attributes[0].Kind != domain.DocAttrFilename || doc.Attributes[0].FileName != "android-a-1.apk" {
		t.Fatalf("document = %+v", doc)
	}
	if string(docs.bytes[doc.ID]) != string(apk) {
		t.Fatal("stored bytes differ from the package")
	}
	if ok, _ := registry.Ready(ctx, file.SHA256); !ok {
		t.Fatal("Ready = false after registration")
	}

	// 重启：新的注册器从 app_update_documents 读到同一个文档，不再读安装包。
	restarted := NewRegistry(source, store, docs, nil)
	again, ready, err := restarted.Document(ctx, file)
	if err != nil || !ready || again.ID != doc.ID {
		t.Fatalf("after restart doc=%+v ready=%v err=%v, want document %d", again, ready, err, doc.ID)
	}
	if got := docs.creates.Load(); got != 1 {
		t.Fatalf("documents created = %d, want 1", got)
	}
}

func TestRegistryConcurrentLookupsRegisterOnce(t *testing.T) {
	apk := []byte("PK\x03\x04 concurrent")
	source := &fakeSource{files: map[string][]byte{"a.apk": apk}, gate: make(chan struct{})}
	docs := newFakeDocuments()
	registry := NewRegistry(source, memory.NewAppUpdateDocumentStore(), docs, nil)
	file := resolvedFile("a.apk", apk)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = registry.Document(context.Background(), file)
		}()
	}
	wg.Wait()
	close(source.gate)
	registry.Wait()
	if got := source.opens.Load(); got != 1 {
		t.Fatalf("package opened %d times, want 1", got)
	}
	if _, ready, _ := registry.Document(context.Background(), file); !ready {
		t.Fatal("not ready after the single registration")
	}
}

func TestRegistryMismatchIsNotRegisteredAndRetriesAfterInterval(t *testing.T) {
	apk := []byte("PK\x03\x04 genuine")
	source := &fakeSource{files: map[string][]byte{"a.apk": []byte("PK\x03\x04 tampered")}}
	docs := newFakeDocuments()
	var now atomic.Int64
	now.Store(time.Unix(1_800_000_000, 0).UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()) }
	registry := NewRegistry(verifyingSource{source}, memory.NewAppUpdateDocumentStore(), docs, nil,
		WithClock(clock), WithIntervals(time.Hour, time.Minute))
	file := resolvedFile("a.apk", apk)
	ctx := context.Background()

	if _, err := registry.Ensure(ctx, file); !errors.Is(err, updatecdn.ErrAppFileMismatch) {
		t.Fatalf("Ensure err = %v, want ErrAppFileMismatch", err)
	}
	if ok, _ := registry.Ready(ctx, file.SHA256); ok {
		t.Fatal("tampered package registered")
	}
	// 失败后重试间隔内不再触发。
	opens := source.opens.Load()
	_, _, _ = registry.Document(ctx, file)
	registry.Wait()
	if source.opens.Load() != opens {
		t.Fatal("retried before the retry interval")
	}
	// 修好文件、过了重试间隔：再次触发就成功。
	source.mu.Lock()
	source.files["a.apk"] = apk
	source.mu.Unlock()
	now.Add(int64(2 * time.Minute))
	_, _, _ = registry.Document(ctx, file)
	registry.Wait()
	if _, ready, _ := registry.Document(ctx, file); !ready {
		t.Fatal("not ready after the retry")
	}
}

func TestRegistryScanRegistersCatalogPackages(t *testing.T) {
	source := &fakeSource{files: map[string][]byte{"a.apk": []byte("PK\x03\x04 a"), "b.apk": []byte("PK\x03\x04 b")}}
	docs := newFakeDocuments()
	registry := NewRegistry(source, memory.NewAppUpdateDocumentStore(), docs, nil)
	if err := registry.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	files, _ := source.AppFiles(context.Background())
	for _, file := range files {
		if ok, _ := registry.Ready(context.Background(), file.SHA256); !ok {
			t.Fatalf("%s not registered by Scan", file.Name)
		}
	}
	// 再扫一次不重复入库。
	if err := registry.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := docs.creates.Load(); got != 2 {
		t.Fatalf("documents created = %d, want 2", got)
	}
	// 来源失败只返回错误。
	source.fail = errors.New("update service down")
	source.files["c.apk"] = []byte("PK\x03\x04 c")
	if err := registry.Scan(context.Background()); err == nil {
		t.Fatal("Scan with a failing source returned nil")
	}
}
