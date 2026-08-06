package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"flag"
	"io"
	"os"
)

func main() {
	input := flag.String("input", "", "executable to archive")
	output := flag.String("output", "", "archive path")
	flag.Parse()
	if flag.NArg() != 0 || *input == "" || *output == "" {
		fatal(errors.New("usage: release-archive --input EXECUTABLE --output ARCHIVE"))
	}

	source, err := os.Open(*input)
	if err != nil {
		fatal(err)
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil {
		fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		fatal(errors.New("input must be an executable regular file"))
	}

	destination, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		fatal(err)
	}
	gzipWriter := gzip.NewWriter(destination)
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{Name: "hctl", Mode: 0o755, Size: info.Size(), Format: tar.FormatUSTAR}
	if err := tarWriter.WriteHeader(header); err != nil {
		fatal(err)
	}
	if _, err := io.Copy(tarWriter, source); err != nil {
		fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		fatal(err)
	}
	if err := destination.Close(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	_, _ = os.Stderr.WriteString("release archive: " + err.Error() + "\n")
	os.Exit(1)
}
