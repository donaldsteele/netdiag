// Command mkzip writes a zip archive whose entries sit at the archive root,
// marking named entries executable (0755) so macOS binaries run after unzip.
//
// Usage: go run ./tools/mkzip -o out.zip [-x name]... file...
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type names []string

func (n *names) String() string     { return strings.Join(*n, ",") }
func (n *names) Set(v string) error { *n = append(*n, v); return nil }

func main() {
	out := flag.String("o", "", "output zip path")
	var exec names
	flag.Var(&exec, "x", "base name of a file to mark executable (repeatable)")
	flag.Parse()
	if *out == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: mkzip -o out.zip [-x name]... file...")
		os.Exit(2)
	}
	if err := write(*out, flag.Args(), exec); err != nil {
		fmt.Fprintln(os.Stderr, "mkzip:", err)
		os.Exit(1)
	}
}

func write(out string, files []string, exec []string) (err error) {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	zw := zip.NewWriter(f)
	for _, path := range files {
		if err := add(zw, path, exec); err != nil {
			return err
		}
	}
	return zw.Close()
}

func add(zw *zip.Writer, path string, exec []string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = filepath.Base(path)
	hdr.Method = zip.Deflate
	mode := os.FileMode(0o644)
	for _, x := range exec {
		if x == hdr.Name {
			mode = 0o755
		}
	}
	hdr.SetMode(mode)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, src)
	return err
}
