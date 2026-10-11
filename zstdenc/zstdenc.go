// Package zstdenc registers zstd as a jarohttp Content-Encoding. It lives in
// its own module so the core jaro module keeps zero dependencies.
package zstdenc

import (
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/lukaszraczylo/jaro/jarohttp"
)

// Register makes jarohttp.Compress prefer zstd over gzip when the client
// accepts it, and lets jarohttp.Decode read zstd request bodies.
func Register() {
	jarohttp.RegisterEncoding(jarohttp.Encoding{
		Name:     "zstd",
		Priority: 2,
		NewWriter: func(w io.Writer) (io.WriteCloser, error) {
			return zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
		},
		NewReader: func(r io.Reader) (io.ReadCloser, error) {
			d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20))
			if err != nil {
				return nil, err
			}
			return d.IOReadCloser(), nil
		},
	})
}
