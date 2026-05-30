// Package extract pulls plain text out of binary document formats (PDF, DOCX)
// shared by the connectors that ingest uploaded files (local, gdrive). Text
// only — no OCR, styling, tables, or images in v0.1/0.2.
package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/dslipak/pdf"
)

// PDF pulls plain text from a PDF's content streams. It handles
// digitally-generated PDFs only — scanned/image PDFs yield no text and OCR is
// out of scope. The underlying parser can panic on malformed input, so a
// recover converts that into a normal error.
func PDF(raw []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pdf parse panic: %v", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", err
	}
	rd, err := r.GetPlainText()
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	if _, err := io.Copy(&buf, rd); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// wordprocessingNS is the OOXML WordprocessingML namespace; .docx text
// elements live under it.
const wordprocessingNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

// DOCX pulls plain text from a .docx file. A .docx is a ZIP archive whose body
// text lives in word/document.xml as <w:t> runs; paragraphs (<w:p>) become
// newlines. Styling, tables, and images are dropped — text only.
func DOCX(raw []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", err
	}
	var body *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			body = f
			break
		}
	}
	if body == nil {
		return "", fmt.Errorf("not a docx: missing word/document.xml")
	}
	rc, err := body.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	var buf strings.Builder
	var inText bool
	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != wordprocessingNS {
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				buf.WriteByte('\t')
			case "br", "cr":
				buf.WriteByte('\n')
			}
		case xml.EndElement:
			if t.Name.Space != wordprocessingNS {
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				buf.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				buf.Write(t)
			}
		}
	}
	return strings.TrimSpace(buf.String()), nil
}
