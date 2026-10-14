// Package testfixture creates original synthetic, credential-free books.
package testfixture

import (
	"archive/zip"
	"fmt"
	"io"
	"strings"
)

func EPUB(w io.Writer) error {
	z := zip.NewWriter(w)
	for _, f := range []struct{ name, body string }{{"mimetype", "application/epub+zip"}, {"META-INF/container.xml", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="OPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`}, {"OPS/content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>合成测试书</dc:title><dc:creator>Test Author</dc:creator><dc:language>zh</dc:language><dc:identifier id="id">synthetic-cwa-test</dc:identifier></metadata><manifest><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/><item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/></manifest><spine toc="ncx"><itemref idref="chapter"/></spine></package>`}, {"OPS/chapter.xhtml", `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>合成测试书</title></head><body><h1>合成测试书</h1><p>这是用于验证上传、转换与下载的原创测试内容。</p></body></html>`}, {"OPS/toc.ncx", `<?xml version="1.0"?><ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1"><head><meta name="dtb:uid" content="synthetic-cwa-test"/></head><docTitle><text>合成测试书</text></docTitle><navMap><navPoint id="c1" playOrder="1"><navLabel><text>测试</text></navLabel><content src="chapter.xhtml"/></navPoint></navMap></ncx>`}} {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		if f.name == "mimetype" {
			h.Method = zip.Store
		}
		out, e := z.CreateHeader(h)
		if e != nil {
			return e
		}
		if _, e = io.WriteString(out, f.body); e != nil {
			return e
		}
	}
	return z.Close()
}

type repeatReader struct{}

func (repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// PDF writes an original valid single-page PDF with a synthetic comment padded
// to exactly size bytes. It uses a fixed buffer for even 1 GiB samples.
func PDF(w io.Writer, size int64) error {
	var small strings.Builder
	small.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R >>", "<< /Length 0 >>\nstream\n\nendstream"} {
		offsets = append(offsets, small.Len())
		fmt.Fprintf(&small, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	start := small.Len()
	fmt.Fprintf(&small, "xref\n0 5\n0000000000 65535 f \n")
	for _, off := range offsets[1:] {
		fmt.Fprintf(&small, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&small, "trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n", start)
	prefix := small.String()
	padding := size - int64(len(prefix)) - 9
	if padding < 0 {
		return fmt.Errorf("PDF size too small")
	}
	if _, e := io.WriteString(w, prefix+"%"); e != nil {
		return e
	}
	if _, e := io.CopyBuffer(w, io.LimitReader(repeatReader{}, padding), make([]byte, 64<<10)); e != nil {
		return e
	}
	_, e := io.WriteString(w, "\n%%EOF\n")
	return e
}
