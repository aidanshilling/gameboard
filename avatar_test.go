package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"testing"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// noise returns a w x h image of random colours: as hard to compress as a photo.
func noise(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rand.New(rand.NewSource(1)).Read(img.Pix)
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255 // opaque
	}
	return img
}

// decodeAs decodes processAvatar's output, checking its format and size.
func decodeAs(t *testing.T, out []byte, format string, size int) image.Image {
	t.Helper()
	img, got, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("output doesn't decode: %v", err)
	}
	if got != format || img.Bounds().Dx() != size || img.Bounds().Dy() != size {
		t.Fatalf("got %s %v, want %dx%d %s", got, img.Bounds(), size, size, format)
	}
	return img
}

func process(t *testing.T, data []byte, wantPixel bool) []byte {
	t.Helper()
	out, pixel, err := processAvatar(data)
	if err != nil {
		t.Fatal(err)
	}
	if pixel != wantPixel {
		t.Fatalf("treated as pixel art = %v, want %v", pixel, wantPixel)
	}
	return out
}

func TestProcessAvatarShrinksBigPhotos(t *testing.T) {
	in := encodePNG(t, noise(4000, 3000))
	out := process(t, in, false)
	decodeAs(t, out, "jpeg", avatarSize)
	t.Logf("%d KB upload -> %d KB stored", len(in)>>10, len(out)>>10)
	if len(out) > 200<<10 {
		t.Errorf("stored %d bytes, want well under 200 KB", len(out))
	}
}

func TestProcessAvatarPhotoTransparencyBecomesWhite(t *testing.T) {
	img := noise(300, 300)
	for y := 0; y < 300; y++ {
		for x := 0; x < 100; x++ {
			img.Pix[img.PixOffset(x, y)+3] = 0 // left third see-through
		}
	}
	out := process(t, encodePNG(t, img), false)
	r, g, b, _ := decodeAs(t, out, "jpeg", avatarSize).At(10, 128).RGBA()
	if r>>8 < 235 || g>>8 < 235 || b>>8 < 235 {
		t.Errorf("transparent area came out as (%d,%d,%d), want white", r>>8, g>>8, b>>8)
	}
}

func TestProcessAvatarSmallPhotoIsScaledUp(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, noise(50, 80), nil); err != nil {
		t.Fatal(err)
	}
	decodeAs(t, process(t, buf.Bytes(), false), "jpeg", avatarSize)
}

func TestProcessAvatarCropsToCentreSquare(t *testing.T) {
	// 300x100: red, green, blue thirds. Only the green middle should survive.
	src := image.NewNRGBA(image.Rect(0, 0, 300, 100))
	for x := 0; x < 300; x++ {
		c := []color.NRGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}}[x/100]
		for y := 0; y < 100; y++ {
			src.Set(x, y, c)
		}
	}
	// Three colours: pixel art, kept at its 100x100 size.
	img := decodeAs(t, process(t, encodePNG(t, src), true), "png", 100)
	for _, x := range []int{0, 50, 99} {
		if got := color.NRGBAModel.Convert(img.At(x, 50)); got != (color.NRGBA{0, 255, 0, 255}) {
			t.Errorf("pixel at x=%d is %v, want pure green", x, got)
		}
	}
}

// sprite is a 32x32 four-colour picture with a see-through corner.
func sprite() *image.NRGBA {
	pal := []color.NRGBA{{20, 20, 40, 255}, {255, 140, 40, 255}, {250, 250, 250, 255}, {0, 0, 0, 0}}
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, pal[(x/4+y/4)%3])
		}
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, pal[3])
		}
	}
	return img
}

func TestProcessAvatarKeepsSmallPixelArtExact(t *testing.T) {
	src := sprite()
	img := decodeAs(t, process(t, encodePNG(t, src), true), "png", 32)
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			if got, want := color.NRGBAModel.Convert(img.At(x, y)), src.At(x, y); got != want {
				t.Fatalf("pixel (%d,%d) = %v, want %v unchanged", x, y, got, want)
			}
		}
	}
}

func TestProcessAvatarShrinksBigPixelArtWithoutBlending(t *testing.T) {
	// The sprite blown up 32x to 1024x1024, as a screenshot of pixel art might be.
	small := sprite()
	big := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			big.Set(x, y, small.At(x/32, y/32))
		}
	}
	img := decodeAs(t, process(t, encodePNG(t, big), true), "png", avatarSize)
	allowed := map[color.NRGBA]bool{}
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			allowed[small.NRGBAAt(x, y)] = true
		}
	}
	for y := 0; y < avatarSize; y++ {
		for x := 0; x < avatarSize; x++ {
			if c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA); !allowed[c] {
				t.Fatalf("pixel (%d,%d) is %v, a blended colour that isn't in the original", x, y, c)
			}
		}
	}
}

func TestProcessAvatarGIF(t *testing.T) {
	var buf bytes.Buffer
	pal := image.NewPaletted(image.Rect(0, 0, 64, 40), []color.Color{color.Black, color.White})
	if err := gif.Encode(&buf, pal, nil); err != nil {
		t.Fatal(err)
	}
	decodeAs(t, process(t, buf.Bytes(), true), "png", 40)
}

func TestProcessAvatarRejects(t *testing.T) {
	// 7000x6000 = 42 megapixels: refused from the header alone, before decoding.
	huge := encodePNG(t, image.NewGray(image.Rect(0, 0, 7000, 6000)))
	if _, _, err := processAvatar(huge); !errors.Is(err, errAvatarPixels) {
		t.Errorf("42MP image: got %v, want errAvatarPixels", err)
	}
	for name, data := range map[string][]byte{
		"empty":   nil,
		"text":    []byte("definitely not a picture"),
		"svg":     []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"cut off": encodePNG(t, image.NewGray(image.Rect(0, 0, 100, 100)))[:60],
	} {
		if _, _, err := processAvatar(data); !errors.Is(err, errAvatarFormat) {
			t.Errorf("%s: got %v, want errAvatarFormat", name, err)
		}
	}
}
