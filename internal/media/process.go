package media

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register WebP decoder
)

const (
	MaxDimension   = 1600 // longest side of the stored image
	ThumbDimension = 480  // longest side of the thumbnail used in grids
	maxPixels      = 40_000_000
	jpegQuality    = 82
)

var ErrTooManyPixels = errors.New("image dimensions are too large")

// processed holds re-encoded image bytes. Re-encoding also strips EXIF metadata
// (camera GPS location etc.) from customer-facing images.
type processed struct {
	main, thumb []byte
	contentType string
	ext         string
}

// process decodes an uploaded image, downsizes it and produces a thumbnail. Images with
// transparency stay PNG; everything else becomes JPEG. Animated GIFs keep only their first frame.
func process(data []byte) (*processed, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrUnsupported
	}
	if cfg.Width*cfg.Height > maxPixels {
		return nil, ErrTooManyPixels
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrUnsupported
	}

	out := &processed{contentType: "image/jpeg", ext: ".jpg"}
	alpha := hasAlpha(src)
	if alpha {
		out.contentType, out.ext = "image/png", ".png"
	}
	if out.main, err = encode(resize(src, MaxDimension), alpha); err != nil {
		return nil, err
	}
	if out.thumb, err = encode(resize(src, ThumbDimension), alpha); err != nil {
		return nil, err
	}
	return out, nil
}

func resize(src image.Image, maxSide int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxSide && h <= maxSide {
		return src
	}
	if w >= h {
		h, w = h*maxSide/w, maxSide
	} else {
		w, h = w*maxSide/h, maxSide
	}
	dst := image.NewRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

func encode(img image.Image, alpha bool) ([]byte, error) {
	var buf bytes.Buffer
	if alpha {
		err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img)
		return buf.Bytes(), err
	}
	// JPEG has no alpha channel: flatten onto white.
	rgb := image.NewRGBA(img.Bounds())
	draw.Draw(rgb, rgb.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(rgb, rgb.Bounds(), img, img.Bounds().Min, draw.Over)
	err := jpeg.Encode(&buf, rgb, &jpeg.Options{Quality: jpegQuality})
	return buf.Bytes(), err
}

// hasAlpha reports whether any pixel is not fully opaque.
func hasAlpha(img image.Image) bool {
	switch m := img.(type) {
	case *image.YCbCr, *image.Gray, *image.CMYK:
		return false
	case *image.Paletted:
		for _, c := range m.Palette {
			if _, _, _, a := c.RGBA(); a != 0xffff {
				return true
			}
		}
		return false
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += max(1, b.Dy()/64) {
		for x := b.Min.X; x < b.Max.X; x += max(1, b.Dx()/64) {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return true
			}
		}
	}
	return false
}

var _ = gif.Decode // GIF decoder registered via the image/gif import
