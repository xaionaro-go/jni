// Command r-consts reads an aapt2-emitted R.txt symbol table and writes a
// deterministic Go source file of `R_<type>_<name>` constants plus styleable
// arrays so Go example code can reach Android resource IDs without going
// through R.java reflection.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/AndroidGoLab/jni/tools/pkg/rconsts"
)

func main() {
	var (
		in     = flag.String("in", "", "path to aapt2-emitted R.txt (required)")
		out    = flag.String("out", "", "path to write the generated .go file (required)")
		pkg    = flag.String("pkg", "", "Go package name for the generated file (required)")
		prefix = flag.String("prefix", "R_", "constant name prefix; full name is <prefix><type>_<name>")
	)
	flag.Parse()

	if err := run(*in, *out, *pkg, *prefix); err != nil {
		log.Fatalf("r-consts: %v", err)
	}
}

func run(in, out, pkg, prefix string) error {
	switch {
	case in == "":
		return fmt.Errorf("--in is required")
	case out == "":
		return fmt.Errorf("--out is required")
	case pkg == "":
		return fmt.Errorf("--pkg is required")
	case prefix == "":
		return fmt.Errorf("--prefix must be non-empty")
	}

	src, err := os.Open(in)
	if err != nil {
		return fmt.Errorf("open %s: %w", in, err)
	}
	defer func() { _ = src.Close() }()

	entries, err := rconsts.Parse(src)
	if err != nil {
		return fmt.Errorf("parse %s: %w", in, err)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(out), err)
	}
	dst, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("create %s: %w", out, err)
	}

	if err := rconsts.Emit(entries, pkg, prefix, dst); err != nil {
		_ = dst.Close()
		return fmt.Errorf("emit %s: %w", out, err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("close %s: %w", out, err)
	}
	return nil
}
