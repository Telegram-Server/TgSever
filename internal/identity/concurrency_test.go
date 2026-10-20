package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 管理进程内并发保存（服务器名称、模板、注册开关）不能互相覆盖：每个 setter 都是
// 读-改-写，两个 Store 实例指向同一目录时也必须串行，否则后写者用旧快照抹掉先写者的字段。
func TestStoreConcurrentSettersDoNotLoseFields(t *testing.T) {
	dir := t.TempDir()
	stores := []*Store{NewStore(dir), NewStore(dir + string(filepath.Separator))}

	const writers = 8
	const rounds = 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds+2)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			s := stores[w%len(stores)]
			for r := 0; r < rounds; r++ {
				if err := s.SetText(fmt.Sprintf("name-%d-%d", w, r), "desc"); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := stores[1].SetSignUpOverride(boolPtr(false)); err != nil {
			errs <- err
		}
		if err := stores[0].SetLoginCodeMessageTemplate("code {{code}}"); err != nil {
			errs <- err
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent save failed: %v", err)
	}

	info, err := stores[0].Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.SignUpEnabled == nil || *info.SignUpEnabled {
		t.Fatalf("sign-up override lost under concurrent saves: %v", info.SignUpEnabled)
	}
	if info.LoginCodeMessageTemplate != "code {{code}}" {
		t.Fatalf("login code template lost under concurrent saves: %q", info.LoginCodeMessageTemplate)
	}
	if !strings.HasPrefix(info.Name, "name-") {
		t.Fatalf("name = %q", info.Name)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != metaFileName {
			t.Fatalf("leftover file after saves: %s", e.Name())
		}
	}
}
