package main

// The binding to libvips: thin C wrappers over the variadic functions of its API, which cgo cannot call, and a Go
// type over VipsImage. Only the worker (worker.go) calls it, one image per process.

/*
#cgo pkg-config: vips
#include <stdlib.h>
#include <vips/vips.h>

// lv_init starts libvips for one image: no operation cache, which would keep pixels between operations of the call,
// and every loader blocked but those of the accepted formats, so that nothing but them ever parses an upload.
static int lv_init(void) {
	if (VIPS_INIT("my-media-libvips"))
		return -1;
	vips_cache_set_max(0);
	vips_block_untrusted_set(TRUE);
	vips_operation_block_set("VipsForeignLoad", TRUE);
	vips_operation_block_set("VipsForeignLoadJpeg", FALSE);
	vips_operation_block_set("VipsForeignLoadPng", FALSE);
	vips_operation_block_set("VipsForeignLoadNsgif", FALSE);
	vips_operation_block_set("VipsForeignLoadWebp", FALSE);
	vips_operation_block_set("VipsForeignLoadHeif", FALSE);
	return 0;
}

// The formats of media.proto (mediav1.Format) that lv_load reads.
enum { LV_JPEG = 1, LV_PNG = 2, LV_GIF = 3, LV_WEBP = 4, LV_AVIF = 9, LV_HEIC = 10 };

// lv_load opens the image at path with the loader of format, reading its header only; the pixels are decoded when an
// operation asks for them. all reads every frame of an animation, as one image of the frames one above the other.
static int lv_load(int format, const char *path, int all, VipsImage **out) {
	int n = all ? -1 : 1;
	switch (format) {
	case LV_JPEG:
		return vips_jpegload(path, out, "fail_on", VIPS_FAIL_ON_ERROR, NULL);
	case LV_PNG:
		return vips_pngload(path, out, "fail_on", VIPS_FAIL_ON_ERROR, NULL);
	case LV_GIF:
		return vips_gifload(path, out, "fail_on", VIPS_FAIL_ON_ERROR, "n", n, NULL);
	case LV_WEBP:
		return vips_webpload(path, out, "fail_on", VIPS_FAIL_ON_ERROR, "n", n, NULL);
	case LV_AVIF:
	case LV_HEIC:
		return vips_heifload(path, out, "fail_on", VIPS_FAIL_ON_ERROR, NULL);
	}
	vips_error("my-media-libvips", "no loader for format %d", format);
	return -1;
}

static int lv_has_icc(VipsImage *in) { return vips_image_get_typeof(in, VIPS_META_ICC_NAME) != 0; }
static int lv_page_height(VipsImage *in) { return vips_image_get_page_height(in); }
static int lv_n_pages(VipsImage *in) { return vips_image_get_n_pages(in); }
static int lv_has_alpha(VipsImage *in) { return vips_image_hasalpha(in); }

static int lv_autorot(VipsImage *in, VipsImage **out) { return vips_autorot(in, out, NULL); }

// lv_srgb converts an image to 8-bit sRGB: through its ICC profile when it has one that works, by its interpretation
// otherwise. An alpha band is kept.
static int lv_srgb(VipsImage *in, int icc, VipsImage **out) {
	if (icc && !vips_icc_transform(in, out, "srgb", "embedded", TRUE, "depth", 8, NULL))
		return 0;
	vips_error_clear();
	VipsImage *t = NULL;
	if (vips_colourspace(in, &t, VIPS_INTERPRETATION_sRGB, NULL))
		return -1;
	if (t->BandFmt == VIPS_FORMAT_UCHAR) {
		*out = t;
		return 0;
	}
	int r = vips_cast(t, out, VIPS_FORMAT_UCHAR, "shift", t->BandFmt == VIPS_FORMAT_USHORT, NULL);
	g_object_unref(t);
	return r;
}

static int lv_extract_band(VipsImage *in, VipsImage **out, int band, int n) {
	return vips_extract_band(in, out, band, "n", n, NULL);
}

static int lv_min(VipsImage *in, double *out) { return vips_min(in, out, NULL); }

static int lv_crop(VipsImage *in, VipsImage **out, int x, int y, int w, int h) {
	return vips_extract_area(in, out, x, y, w, h, NULL);
}

// lv_resize scales an 8-bit image by hscale × vscale with Lanczos, its colors premultiplied by its alpha if it has one,
// so that transparent pixels do not bleed into their neighbours.
static int lv_resize(VipsImage *in, VipsImage **out, double hscale, double vscale) {
	VipsImage *t[4] = {NULL};
	VipsImage *x = in;
	int alpha = vips_image_hasalpha(in);
	int r = -1;
	if (alpha) {
		if (vips_premultiply(x, &t[0], NULL))
			goto done;
		x = t[0];
	}
	if (vips_resize(x, &t[1], hscale, "vscale", vscale, "kernel", VIPS_KERNEL_LANCZOS3, NULL))
		goto done;
	x = t[1];
	if (alpha) {
		if (vips_unpremultiply(x, &t[2], NULL))
			goto done;
		x = t[2];
	}
	if (vips_cast(x, &t[3], VIPS_FORMAT_UCHAR, NULL))
		goto done;
	*out = t[3];
	t[3] = NULL;
	r = 0;
done:
	for (int i = 0; i < 4; i++)
		if (t[i])
			g_object_unref(t[i]);
	return r;
}

static int lv_arrayjoin(VipsImage **in, int n, VipsImage **out) {
	return vips_arrayjoin(in, out, n, "across", 1, NULL);
}

// lv_pages copies an image with the height of its pages set, which an animation's saver reads; 0 makes it a single
// page and removes the metadata of animation.
static int lv_pages(VipsImage *in, VipsImage **out, int page_height) {
	if (vips_copy(in, out, NULL))
		return -1;
	if (page_height > 0) {
		vips_image_set_int(*out, VIPS_META_PAGE_HEIGHT, page_height);
		return 0;
	}
	vips_image_remove(*out, VIPS_META_PAGE_HEIGHT);
	vips_image_remove(*out, VIPS_META_N_PAGES);
	vips_image_remove(*out, "delay");
	vips_image_remove(*out, "gif-delay");
	vips_image_remove(*out, "loop");
	vips_image_remove(*out, "gif-loop");
	return 0;
}

// lv_to_temp computes an image into a temporary file of TMPDIR, which is removed when the image is unreferenced, so
// that the operations after it read its pixels without decoding them again.
static int lv_to_temp(VipsImage *in, VipsImage **out) {
	VipsImage *t = vips_image_new_temp_file("%s.v");
	if (!t)
		return -1;
	if (vips_image_write(in, t)) {
		g_object_unref(t);
		return -1;
	}
	*out = t;
	return 0;
}

// The savers write no metadata: no EXIF, XMP, IPTC, ICC profile, or comment.
static int lv_webpsave(VipsImage *in, const char *path, int q) {
	return vips_webpsave(in, path, "Q", q, "keep", VIPS_FOREIGN_KEEP_NONE, NULL);
}
static int lv_jpegsave(VipsImage *in, const char *path, int q) {
	return vips_jpegsave(in, path, "Q", q, "keep", VIPS_FOREIGN_KEEP_NONE, "optimize_coding", TRUE,
		"subsample_mode", VIPS_FOREIGN_SUBSAMPLE_ON, NULL);
}
static int lv_pngsave(VipsImage *in, const char *path) {
	return vips_pngsave(in, path, "keep", VIPS_FOREIGN_KEEP_NONE, "compression", 6, NULL);
}
static int lv_gifsave(VipsImage *in, const char *path) {
	return vips_gifsave(in, path, "keep", VIPS_FOREIGN_KEEP_NONE, "dither", 1.0, NULL);
}

static int lv_has_operation(const char *nickname) { return vips_type_find("VipsOperation", nickname) != 0; }
*/
import "C"

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

// vipsInit starts libvips in this process. threads is the size of its pool of threads; 0 leaves its default.
func vipsInit(threads int) error {
	if C.lv_init() != 0 {
		return vipsError("start libvips")
	}
	if threads > 0 {
		C.vips_concurrency_set(C.int(threads))
	}
	return nil
}

// vipsVersion returns the version of the libvips the process runs with.
func vipsVersion() (major, minor, patch int) {
	return int(C.vips_version(0)), int(C.vips_version(1)), int(C.vips_version(2))
}

func vipsHasOperation(nickname string) bool {
	cs := C.CString(nickname)
	defer C.free(unsafe.Pointer(cs))
	return C.lv_has_operation(cs) != 0
}

// errVips is a failure of libvips, with the messages of its error buffer.
type errVips struct{ msg string }

func (e *errVips) Error() string { return e.msg }

// vipsError takes the messages of the error buffer of libvips, and clears it.
func vipsError(op string) error {
	msg := strings.TrimSpace(C.GoString(C.vips_error_buffer()))
	C.vips_error_clear()
	return &errVips{fmt.Sprintf("%s: %s", op, msg)}
}

// vimage is a VipsImage. Every image a call makes goes in its arena, which unreferences them at the end.
type vimage struct {
	p *C.VipsImage
	a *arena
}

type arena struct{ images []*C.VipsImage }

func (a *arena) add(p *C.VipsImage) *vimage {
	a.images = append(a.images, p)
	return &vimage{p, a}
}

func (a *arena) free() {
	for _, p := range a.images {
		C.g_object_unref(C.gpointer(p))
	}
	a.images = nil
}

func (a *arena) load(format int, path string, all bool) (*vimage, error) {
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	var out *C.VipsImage
	if C.lv_load(C.int(format), cs, cBool(all), &out) != 0 {
		return nil, vipsError("load")
	}
	return a.add(out), nil
}

func (im *vimage) width() int      { return int(im.p.Xsize) }
func (im *vimage) height() int     { return int(im.p.Ysize) }
func (im *vimage) bands() int      { return int(im.p.Bands) }
func (im *vimage) hasICC() bool    { return C.lv_has_icc(im.p) != 0 }
func (im *vimage) hasAlpha() bool  { return C.lv_has_alpha(im.p) != 0 }
func (im *vimage) pageHeight() int { return int(C.lv_page_height(im.p)) }
func (im *vimage) pages() int      { return int(C.lv_n_pages(im.p)) }

// op runs one operation of libvips that makes an image of im.
func (im *vimage) op(name string, f func(out **C.VipsImage) C.int) (*vimage, error) {
	var out *C.VipsImage
	if f(&out) != 0 {
		return nil, vipsError(name)
	}
	return im.a.add(out), nil
}

func (im *vimage) autorot() (*vimage, error) {
	return im.op("autorot", func(out **C.VipsImage) C.int { return C.lv_autorot(im.p, out) })
}

func (im *vimage) srgb() (*vimage, error) {
	return im.op("srgb", func(out **C.VipsImage) C.int { return C.lv_srgb(im.p, cBool(im.hasICC()), out) })
}

func (im *vimage) extractBands(band, n int) (*vimage, error) {
	return im.op("extract_band", func(out **C.VipsImage) C.int { return C.lv_extract_band(im.p, out, C.int(band), C.int(n)) })
}

func (im *vimage) min() (float64, error) {
	var v C.double
	if C.lv_min(im.p, &v) != 0 {
		return 0, vipsError("min")
	}
	return float64(v), nil
}

func (im *vimage) crop(x, y, w, h int) (*vimage, error) {
	return im.op("crop", func(out **C.VipsImage) C.int {
		return C.lv_crop(im.p, out, C.int(x), C.int(y), C.int(w), C.int(h))
	})
}

// resize scales the image to exactly w × h.
func (im *vimage) resize(w, h int) (*vimage, error) {
	hs, vs := float64(w)/float64(im.width()), float64(h)/float64(im.height())
	out, err := im.op("resize", func(out **C.VipsImage) C.int { return C.lv_resize(im.p, out, C.double(hs), C.double(vs)) })
	if err != nil {
		return nil, err
	}
	if out.width() != w || out.height() != h {
		return nil, fmt.Errorf("resize: %d×%d, not %d×%d", out.width(), out.height(), w, h)
	}
	return out, nil
}

// withPages sets the height of the pages of an animation; 0 makes the image a single page.
func (im *vimage) withPages(pageHeight int) (*vimage, error) {
	return im.op("copy", func(out **C.VipsImage) C.int { return C.lv_pages(im.p, out, C.int(pageHeight)) })
}

func (im *vimage) toTemp() (*vimage, error) {
	return im.op("write", func(out **C.VipsImage) C.int { return C.lv_to_temp(im.p, out) })
}

// joinPages stacks the frames, which share their size, one above the other, as an animation.
func joinPages(frames []*vimage) (*vimage, error) {
	if len(frames) == 0 {
		return nil, errors.New("arrayjoin: no frame")
	}
	arr := (*[1 << 28]*C.VipsImage)(C.malloc(C.size_t(len(frames)) * C.size_t(unsafe.Sizeof((*C.VipsImage)(nil)))))[:len(frames):len(frames)]
	defer C.free(unsafe.Pointer(&arr[0]))
	for i, f := range frames {
		arr[i] = f.p
	}
	joined, err := frames[0].op("arrayjoin", func(out **C.VipsImage) C.int { return C.lv_arrayjoin(&arr[0], C.int(len(frames)), out) })
	if err != nil {
		return nil, err
	}
	return joined.withPages(frames[0].height())
}

// rgb returns the pixels of an 8-bit image of 3 bands, row by row.
func (im *vimage) rgb() ([]byte, error) {
	var n C.size_t
	p := C.vips_image_write_to_memory(im.p, &n)
	if p == nil {
		return nil, vipsError("write_to_memory")
	}
	defer C.g_free(C.gpointer(p))
	return C.GoBytes(p, C.int(n)), nil
}

func (im *vimage) save(saver string, path string, quality int) error {
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	var r C.int
	switch saver {
	case "webp":
		r = C.lv_webpsave(im.p, cs, C.int(quality))
	case "jpeg":
		r = C.lv_jpegsave(im.p, cs, C.int(quality))
	case "png":
		r = C.lv_pngsave(im.p, cs)
	case "gif":
		r = C.lv_gifsave(im.p, cs)
	default:
		return fmt.Errorf("no saver %q", saver)
	}
	if r != 0 {
		return vipsError(saver + "save")
	}
	return nil
}

func cBool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
