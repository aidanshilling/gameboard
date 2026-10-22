package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"time"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Profile pictures. Whatever is uploaded is decoded, cropped to a square and
// re-encoded; only that small copy is stored. The upload itself is never
// kept, and re-encoding drops metadata (such as a photo's GPS location) and
// anything else hidden in the file.
//
// Photos are smoothly resized to avatarSize and saved as JPEG. Pixel art
// (few colours) would be smeared by that, so it's kept sharp instead: stored
// at its own size if it's small, or shrunk without blending pixels if not,
// and saved as a lossless PNG, transparency and all.
//
// The browser sends the part of the picture the person chose, already square,
// but the server doesn't rely on that: it handles any size it can decode.

const (
	avatarSize      = 256
	avatarMaxUpload = 15 << 20 // bytes accepted in one upload
	avatarMaxPixels = 40e6     // bigger images are refused rather than decoded
	avatarMaxStored = 5 << 20  // never store more than this (in practice ~20 KB)
)

// avatarSlots limits how many uploads are decoded at once: a big image takes
// a lot of memory, and the server is small.
var avatarSlots = make(chan struct{}, 2)

var (
	errAvatarFormat = errors.New("that file isn't a picture we can read; try a JPG, PNG, GIF or WebP")
	errAvatarPixels = fmt.Errorf("that picture is too large to process (over %d megapixels)", int(avatarMaxPixels/1e6))
	errAvatarStored = errors.New("couldn't compress that picture small enough")
)

// pixelArtColours is the most distinct colours a picture can use and still
// be treated as pixel art.
const pixelArtColours = 256

// processAvatar turns an uploaded image into what gets stored, and reports
// whether it was treated as pixel art (so it should be shown unsmoothed).
func processAvatar(data []byte) (out []byte, pixel bool, err error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, false, errAvatarFormat
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || float64(cfg.Width)*float64(cfg.Height) > avatarMaxPixels {
		return nil, false, errAvatarPixels
	}

	avatarSlots <- struct{}{}
	defer func() { <-avatarSlots }()
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false, errAvatarFormat
	}

	// Take the centre square.
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	square := image.Rect(x0, y0, x0+side, y0+side)

	// Counting colours is only worth it at sizes pixel art comes in.
	if side <= 2048 && fewColours(src, square, pixelArtColours) {
		out, err := pixelAvatar(src, square)
		return out, true, err
	}
	out, err = photoAvatar(src, square)
	return out, false, err
}

// photoAvatar smoothly scales the square to avatarSize and saves a JPEG.
func photoAvatar(src image.Image, square image.Rectangle) ([]byte, error) {
	dst := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	// JPEG has no transparency, so see-through parts become white, not black.
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, square, draw.Over, nil)

	var buf bytes.Buffer
	for _, quality := range []int{85, 70, 50} {
		buf.Reset()
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		if buf.Len() <= avatarMaxStored {
			return buf.Bytes(), nil
		}
	}
	return nil, errAvatarStored
}

// pixelAvatar keeps pixel art sharp: at its own size if it already fits,
// otherwise shrunk by picking pixels (never blending them). Saved as PNG.
func pixelAvatar(src image.Image, square image.Rectangle) ([]byte, error) {
	size := min(square.Dx(), avatarSize)
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.NearestNeighbor.Scale(dst, dst.Bounds(), src, square, draw.Src, nil)

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, dst); err != nil {
		return nil, err
	}
	if buf.Len() > avatarMaxStored {
		return nil, errAvatarStored
	}
	return buf.Bytes(), nil
}

// fewColours reports whether the area uses at most n distinct colours.
func fewColours(img image.Image, area image.Rectangle, n int) bool {
	seen := make(map[color.NRGBA]struct{}, n+1)
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			seen[color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)] = struct{}{}
			if len(seen) > n {
				return false
			}
		}
	}
	return true
}

// handleSetAvatar replaces the caller's picture with an uploaded image (raw body).
func (a *App) handleSetAvatar(w http.ResponseWriter, r *http.Request, u *User) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, avatarMaxUpload))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("that file is over %d MB", avatarMaxUpload>>20))
			return
		}
		writeErr(w, http.StatusBadRequest, "upload failed")
		return
	}
	img, pixel, err := processAvatar(data)
	switch {
	case errors.Is(err, errAvatarFormat), errors.Is(err, errAvatarPixels), errors.Is(err, errAvatarStored):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		serverErr(w, err)
		return
	}
	// The version (milliseconds) goes in the picture's URL, so browsers can
	// cache each one forever and still pick up a new one immediately.
	version := time.Now().UnixMilli()
	if _, err := a.db.Exec(`INSERT INTO avatars(user_id, image, version, pixel) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET image = excluded.image, version = excluded.version, pixel = excluded.pixel`,
		u.ID, img, version, pixel); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"avatar": version, "pixel": pixel, "bytes": len(img)})
}

func (a *App) handleDeleteAvatar(w http.ResponseWriter, r *http.Request, u *User) {
	if _, err := a.db.Exec(`DELETE FROM avatars WHERE user_id = ?`, u.ID); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdminDeleteAvatar lets an admin take down anyone's picture.
func (a *App) handleAdminDeleteAvatar(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := a.db.Exec(`DELETE FROM avatars WHERE user_id = ?`, id); err != nil {
		serverErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleGetAvatar serves a user's picture. URLs carry ?v=<version>, so the
// response can be cached for good.
func (a *App) handleGetAvatar(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var img []byte
	err := a.db.QueryRow(`SELECT image FROM avatars WHERE user_id = ?`, id).Scan(&img)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "no picture")
		return
	}
	if err != nil {
		serverErr(w, err)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(img)) // JPEG or PNG
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Write(img)
}
