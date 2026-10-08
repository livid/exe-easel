package main

// Smaller copies of looks and finished paintings: a JPEG at most w pixels
// wide, by an area-average downscale (each output pixel the mean of the
// source pixels its box covers, partial pixels weighted), stdlib only.

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
)

func decodeFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// downscale returns src at most w wide (keeping its shape); smaller images
// come back as they are, as RGBA.
func downscale(src image.Image, w int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	rgba := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	if w <= 0 || sw <= w {
		return rgba
	}
	h := int(float64(sh)*float64(w)/float64(sw) + 0.5)
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	fx := float64(sw) / float64(w)
	fy := float64(sh) / float64(h)
	for y := 0; y < h; y++ {
		y0, y1 := float64(y)*fy, float64(y+1)*fy
		for x := 0; x < w; x++ {
			x0, x1 := float64(x)*fx, float64(x+1)*fx
			var r, g, bl, a, wt float64
			for sy := int(y0); sy < sh && float64(sy) < y1; sy++ {
				cy := min(y1, float64(sy+1)) - max(y0, float64(sy))
				if cy <= 0 {
					continue
				}
				row := sy * rgba.Stride
				for sx := int(x0); sx < sw && float64(sx) < x1; sx++ {
					cx := min(x1, float64(sx+1)) - max(x0, float64(sx))
					if cx <= 0 {
						continue
					}
					k := cx * cy
					i := row + sx*4
					p := rgba.Pix[i : i+4 : i+4]
					r += float64(p[0]) * k
					g += float64(p[1]) * k
					bl += float64(p[2]) * k
					a += float64(p[3]) * k
					wt += k
				}
			}
			o := dst.PixOffset(x, y)
			dst.Pix[o] = uint8(r/wt + 0.5)
			dst.Pix[o+1] = uint8(g/wt + 0.5)
			dst.Pix[o+2] = uint8(bl/wt + 0.5)
			dst.Pix[o+3] = uint8(a/wt + 0.5)
		}
	}
	return dst
}

// writeJPEG writes src (a PNG or JPEG file) as a JPEG at most w wide.
func writeJPEG(src, dst string, w, quality int) error {
	img, err := decodeFile(src)
	if err != nil {
		return err
	}
	out := downscale(img, w)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// Each writer has a .part of its own: two requests for one thumbnail at
	// once used to share one, and the second's rename found it gone.
	f, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*.part")
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = jpeg.Encode(f, out, &jpeg.Options{Quality: quality})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// thumbnail returns the path of a cached JPEG copy of the studio file at
// most w wide, making it if needed. The cache key holds the file's size and
// time, so a changed file (final.png after another finish) gets a new one.
func thumbnail(studio, rel, abs string, w int) (string, error) {
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d|%d", rel, fi.Size(), fi.ModTime().UnixNano(), w)))
	dst := filepath.Join(studio, "out/app/thumbs", hex.EncodeToString(h[:])+".jpg")
	if exists(dst) {
		return dst, nil
	}
	return dst, writeJPEG(abs, dst, w, 85)
}
