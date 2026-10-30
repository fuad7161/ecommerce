package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestProcessResizesAndConvertsOpaqueToJPEG(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3200, 1600))
	for i := range src.Pix {
		src.Pix[i] = 0xff
	}
	out, err := process(encodePNG(t, src))
	if err != nil {
		t.Fatal(err)
	}
	if out.contentType != "image/jpeg" {
		t.Fatalf("opaque image should become JPEG, got %s", out.contentType)
	}
	main, err := jpeg.Decode(bytes.NewReader(out.main))
	if err != nil {
		t.Fatal(err)
	}
	if b := main.Bounds(); b.Dx() != MaxDimension || b.Dy() != 800 {
		t.Fatalf("main size %v", b)
	}
	thumb, _ := jpeg.Decode(bytes.NewReader(out.thumb))
	if b := thumb.Bounds(); b.Dx() != ThumbDimension || b.Dy() != 240 {
		t.Fatalf("thumb size %v", b)
	}
}

func TestProcessKeepsTransparencyAsPNG(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	src.Set(10, 10, color.NRGBA{255, 0, 0, 128})
	out, err := process(encodePNG(t, src))
	if err != nil || out.contentType != "image/png" || out.ext != ".png" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestProcessRejectsGarbage(t *testing.T) {
	for _, junk := range []string{"GIF89a not really", "\x89PNG\r\n\x1a\nbroken", "plain text"} {
		if _, err := process([]byte(junk)); !IsUserError(err) {
			t.Fatalf("%q: got %v", junk, err)
		}
	}
}

func TestThumbKeys(t *testing.T) {
	if ThumbKey("products/1/abc.jpg") != "products/1/abc_thumb.jpg" {
		t.Fatal(ThumbKey("products/1/abc.jpg"))
	}
	if k, ok := MainKey("products/1/abc_thumb.jpg"); !ok || k != "products/1/abc.jpg" {
		t.Fatal(k, ok)
	}
	if _, ok := MainKey("products/1/abc.jpg"); ok {
		t.Fatal("not a thumb key")
	}
}
