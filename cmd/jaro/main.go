// Command jaro converts between JSON and JARO and validates data.
//
//	jaro enc [-b] [-dedup] [file]     JSON -> JARO text (or binary with -b)
//	jaro dec [-b] [file]              JARO -> JSON
//	jaro check -s schema.jaro [-b] [file]   validate JARO (or JSON with -j)
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lukaszraczylo/jaro"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	bin := fs.Bool("b", false, "binary JARO")
	dedup := fs.Bool("dedup", false, "enc: anchor repeated sub-trees")
	schema := fs.String("s", "", "check: schema file (JARO text)")
	asJSON := fs.Bool("j", false, "check: input is JSON")
	fatal(fs.Parse(args))
	in, err := readInput(fs.Arg(0))
	fatal(err)
	switch cmd {
	case "enc":
		v, err := jaro.FromJSON(in)
		fatal(err)
		o := jaro.Options{Dedup: *dedup}
		var out []byte
		if *bin {
			out, err = jaro.MarshalBinaryOpts(v, o)
		} else {
			out, err = jaro.MarshalOpts(v, o)
		}
		fatal(err)
		_, err = os.Stdout.Write(out)
		fatal(err)
	case "dec":
		v, err := decode(in, *bin)
		fatal(err)
		out, err := jaro.ToJSON(v)
		fatal(err)
		_, err = os.Stdout.Write(append(out, '\n'))
		fatal(err)
	case "check":
		if *schema == "" {
			usage()
		}
		sb, err := os.ReadFile(filepath.Clean(*schema))
		fatal(err)
		def, err := jaro.Unmarshal(sb)
		fatal(err)
		s, err := jaro.Compile(def)
		fatal(err)
		var v any
		if *asJSON {
			v, err = jaro.FromJSON(in)
		} else {
			v, err = decode(in, *bin)
		}
		fatal(err)
		errs := s.Validate(v)
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, e)
		}
		if len(errs) > 0 {
			os.Exit(1)
		}
		fmt.Println("ok")
	default:
		usage()
	}
}

func decode(in []byte, bin bool) (any, error) {
	if bin {
		return jaro.UnmarshalBinary(in)
	}
	return jaro.Unmarshal(in)
}

func readInput(path string) ([]byte, error) {
	if path == "" || path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(filepath.Clean(path))
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "jaro:", err)
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: jaro enc|dec|check [-b] [-dedup] [-s schema] [-j] [file]")
	os.Exit(2)
}
