package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Many requests for one thumbnail at once (the app asking for a studio's
// looks while another window does) each get it, and leave no .part behind.
func TestThumbnailAtOnce(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "out/easel/painting/look.png")
	os.MkdirAll(filepath.Dir(src), 0o755)
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 400, 300)))
	f.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := thumbnail(dir, "out/easel/painting/look.png", src, 100); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "out/app/thumbs/*"))
	if len(left) != 1 || filepath.Ext(left[0]) != ".jpg" {
		t.Fatalf("thumbs folder holds %v", left)
	}
	if fi, _ := os.Stat(left[0]); fi.Mode().Perm() != 0o644 {
		t.Fatalf("thumbnail mode %v", fi.Mode())
	}
}
