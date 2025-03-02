// Copyright 2016 - 2022 The excelize Authors. All rights reserved. Use of
// this source code is governed by a BSD-style license that can be found in
// the LICENSE file.
//
// Package excelize providing a set of functions that allow you to write to and
// read from XLAM / XLSM / XLSX / XLTM / XLTX files. Supports reading and
// writing spreadsheet documents generated by Microsoft Excel™ 2007 and later.
// Supports complex components by high compatibility, and provided streaming
// API for generating or reading data from a worksheet with huge amounts of
// data. This library needs Go version 1.15 or later.

package excelize

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// NewFile provides a function to create new file by default template.
// For example:
//
//	f := NewFile()
func NewFile() *File {
	f := newFile()
	f.Pkg.Store("_rels/.rels", []byte(xml.Header+templateRels))
	f.Pkg.Store(defaultXMLPathDocPropsApp, []byte(xml.Header+templateDocpropsApp))
	f.Pkg.Store(defaultXMLPathDocPropsCore, []byte(xml.Header+templateDocpropsCore))
	f.Pkg.Store("xl/_rels/workbook.xml.rels", []byte(xml.Header+templateWorkbookRels))
	f.Pkg.Store("xl/theme/theme1.xml", []byte(xml.Header+templateTheme))
	f.Pkg.Store("xl/worksheets/sheet1.xml", []byte(xml.Header+templateSheet))
	f.Pkg.Store(defaultXMLPathStyles, []byte(xml.Header+templateStyles))
	f.Pkg.Store(defaultXMLPathWorkbook, []byte(xml.Header+templateWorkbook))
	f.Pkg.Store(defaultXMLPathContentTypes, []byte(xml.Header+templateContentTypes))
	f.SheetCount = 1
	f.CalcChain = f.calcChainReader()
	f.Comments = make(map[string]*xlsxComments)
	f.ContentTypes = f.contentTypesReader()
	f.Drawings = sync.Map{}
	f.Styles = f.stylesReader()
	f.DecodeVMLDrawing = make(map[string]*decodeVmlDrawing)
	f.VMLDrawing = make(map[string]*vmlDrawing)
	f.WorkBook = f.workbookReader()
	f.Relationships = sync.Map{}
	f.Relationships.Store("xl/_rels/workbook.xml.rels", f.relsReader("xl/_rels/workbook.xml.rels"))
	f.sheetMap["Sheet1"] = "xl/worksheets/sheet1.xml"
	ws, _ := f.workSheetReader("Sheet1")
	f.Sheet.Store("xl/worksheets/sheet1.xml", ws)
	f.Theme = f.themeReader()
	return f
}

// Save provides a function to override the spreadsheet with origin path.
func (f *File) Save(opts ...Options) error {
	if f.Path == "" {
		return ErrSave
	}
	for i := range opts {
		f.options = &opts[i]
	}
	return f.SaveAs(f.Path, *f.options)
}

// SaveAs provides a function to create or update to a spreadsheet at the
// provided path.
func (f *File) SaveAs(name string, opts ...Options) error {
	if len(name) > MaxFilePathLength {
		return ErrMaxFilePathLength
	}
	f.Path = name
	if _, ok := supportedContentTypes[filepath.Ext(f.Path)]; !ok {
		return ErrWorkbookFileFormat
	}
	file, err := os.OpenFile(filepath.Clean(name), os.O_WRONLY|os.O_TRUNC|os.O_CREATE, os.ModePerm)
	if err != nil {
		return err
	}
	defer file.Close()
	return f.Write(file, opts...)
}

// Close closes and cleanup the open temporary file for the spreadsheet.
func (f *File) Close() error {
	var err error
	if f.sharedStringTemp != nil {
		if err := f.sharedStringTemp.Close(); err != nil {
			return err
		}
	}
	f.tempFiles.Range(func(k, v interface{}) bool {
		if err = os.Remove(v.(string)); err != nil {
			return false
		}
		return true
	})
	for _, stream := range f.streams {
		_ = stream.rawData.Close()
	}
	return err
}

// Write provides a function to write to an io.Writer.
func (f *File) Write(w io.Writer, opts ...Options) error {
	_, err := f.WriteTo(w, opts...)
	return err
}

// WriteTo implements io.WriterTo to write the file.
func (f *File) WriteTo(w io.Writer, opts ...Options) (int64, error) {
	for i := range opts {
		f.options = &opts[i]
	}
	if len(f.Path) != 0 {
		contentType, ok := supportedContentTypes[filepath.Ext(f.Path)]
		if !ok {
			return 0, ErrWorkbookFileFormat
		}
		f.setContentTypePartProjectExtensions(contentType)
	}
	if f.options != nil && f.options.Password != "" {
		buf, err := f.WriteToBuffer()
		if err != nil {
			return 0, err
		}
		return buf.WriteTo(w)
	}
	if err := f.writeDirectToWriter(w); err != nil {
		return 0, err
	}
	return 0, nil
}

// WriteToBuffer provides a function to get bytes.Buffer from the saved file,
// and it allocates space in memory. Be careful when the file size is large.
func (f *File) WriteToBuffer() (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	if err := f.writeToZip(zw); err != nil {
		return buf, zw.Close()
	}

	if f.options != nil && f.options.Password != "" {
		if err := zw.Close(); err != nil {
			return buf, err
		}
		b, err := Encrypt(buf.Bytes(), f.options)
		if err != nil {
			return buf, err
		}
		buf.Reset()
		buf.Write(b)
		return buf, nil
	}
	return buf, zw.Close()
}

// writeDirectToWriter provides a function to write to io.Writer.
func (f *File) writeDirectToWriter(w io.Writer) error {
	zw := zip.NewWriter(w)
	if err := f.writeToZip(zw); err != nil {
		_ = zw.Close()
		return err
	}
	return zw.Close()
}

// writeToZip provides a function to write to zip.Writer
func (f *File) writeToZip(zw *zip.Writer) error {
	f.calcChainWriter()
	f.commentsWriter()
	f.contentTypesWriter()
	f.drawingsWriter()
	f.vmlDrawingWriter()
	f.workBookWriter()
	f.workSheetWriter()
	f.relsWriter()
	_ = f.sharedStringsLoader()
	f.sharedStringsWriter()
	f.styleSheetWriter()

	for path, stream := range f.streams {
		fi, err := zw.Create(path)
		if err != nil {
			return err
		}
		var from io.Reader
		from, err = stream.rawData.Reader()
		if err != nil {
			_ = stream.rawData.Close()
			return err
		}
		_, err = io.Copy(fi, from)
		if err != nil {
			return err
		}
	}
	var err error
	f.Pkg.Range(func(path, content interface{}) bool {
		if err != nil {
			return false
		}
		if _, ok := f.streams[path.(string)]; ok {
			return true
		}
		var fi io.Writer
		fi, err = zw.Create(path.(string))
		if err != nil {
			return false
		}
		_, err = fi.Write(content.([]byte))
		return true
	})
	f.tempFiles.Range(func(path, content interface{}) bool {
		if _, ok := f.Pkg.Load(path); ok {
			return true
		}
		var fi io.Writer
		fi, err = zw.Create(path.(string))
		if err != nil {
			return false
		}
		_, err = fi.Write(f.readBytes(path.(string)))
		return true
	})
	return err
}
