package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"

	log "github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"

	"github.com/depp/spicy"
	"github.com/trhodeos/n64rom"
)

var (
	verbose                           = flag.BoolP("verbose", "d", false, "print verbose information")
	link_editor_verbose               = flag.BoolP("verbose_linking", "m", false, "print verbose information when link editing")
	disable_overlapping_section_check = flag.BoolP("disable_overlapping_section_checks", "o", false, "disable checks for overlapping sections")
	romsize_mbits                     = flag.IntP("romsize", "s", -1, "ROM size (Mbit)")
	filldata                          = flag.IntP("filldata_byte", "f", 0x0, "fill byte for data in the ROM image")
	bootstrap_filename                = flag.StringP("bootstrap_file", "b", "Boot", "bootstrap file (not currently used)")
	header_filename                   = flag.StringP("romheader_file", "h", "romheader", "header file (not currently used)")
	pif_bootstrap_filename            = flag.StringP("pif2boot_file", "p", "pif2Boot", "PIF bootstrap file (not currently used)")
	rom_image_file                    = flag.StringP("rom_name", "r", "rom.n64", "output ROM image filename")
	elf_file                          = flag.StringP("rom_elf_name", "e", "rom.out", "output ROM image filename")
	defineFlags                       = flag.StringArrayP("define", "D", nil, "macro definition for preprocessor")
	includeFlags                      = flag.StringArrayP("include", "I", nil, "header search path for preprocessor")
	undefineFlags                     = flag.StringArrayP("undefine", "U", nil, "macros to undefine in preprocessor")

	// Non-standard options. Should all be optional.
	ld_command      = flag.String("ld_command", "mips64-elf-ld", "ld command to use")
	as_command      = flag.String("as_command", "mips64-elf-as", "as command to use")
	cpp_command     = flag.String("cpp_command", "mips64-elf-gcc", "cpp command to use")
	objcopy_command = flag.String("objcopy_command", "mips64-elf-objcopy", "objcopy command to use")
	font_filename   = flag.String("font_filename", "font", "Font filename")
)

/*
-Dname[=def] Is passed to cpp(1) for use during its invocation.
-Idirectory Is passed to cpp(1) for use during its invocation.
Uname Is passed to cpp(1) for use during its invocation.
-d Gives a verbose account of all the actions that makerom takes, leaving temporary files created that are ordinarily deleted.
-m Prints a link editor map to standard output for diagnostic purposes.
-o Disables checking of overlapping sections. By default, segments with direct-mapped CPU addresses are checked to ensure that the underlying physical memory mappings do not conflict.
-b <bootstrap filename> Overrides the default filename (/usr/lib/PR/Boot). This file must be in COFF format, and is loaded as 1K bytes into the ramrom memory.
-h <header filename> Overrides the default ROM header filename (/usr/lib/PR/romheader). This file is in ASCII format, and each character is converted to hex and loaded in sequence, starting at the beginning of ramrom memory. Currently only 32 bytes are used.
-s <romsize (Mbits)> Creates a ROM image with the specified size. This option is required for making the real Game Pak.
-f <filldata (0x0 - 0xff)> Sets the fill pattern for "holes" within a ROM image. The argument filldata is a one-byte hexadecimal constant. Use this option when you create a ROM image using the -s option. It is required for making the real Game Pak.
-p <pif bootstrap file> Overrides the pif bootstrap filename (/usr/lib/PR/pif2Boot). This file must be in COFF format. It is loaded as 4K bytes into the ramrom memory.
-r Provides an alternate ROM image file; the default is 'rom'.
-B 0 An option that concerns only games supported by 64DD. Using this option creates a startup game. For information on startup games, please see Section 15.1, "Restarting," in the N64 Disk Drive Programming Manual.
*/

func mainE() error {
	flag.Parse()
	if flag.NArg() != 1 {
		if flag.NArg() == 0 {
			return errors.New("missing argument: <spec>")
		}
		return fmt.Errorf("invalid usage: got %d arguments, expected exactly 1", flag.NArg())
	}
	if *verbose {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.WarnLevel)
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		return fmt.Errorf("could not open spec: %v", err)
	}
	defer f.Close()

	gcc := spicy.NewRunner(*cpp_command)
	ld := spicy.NewRunner(*ld_command)
	as := spicy.NewRunner(*as_command)
	objcopy := spicy.NewRunner(*objcopy_command)
	preprocessed, err := spicy.PreprocessSpec(f, gcc, *includeFlags, *defineFlags, *undefineFlags)
	if err != nil {
		return fmt.Errorf("could not preprocess spec: %v", err)
	}
	spec, err := spicy.ParseSpec(preprocessed)
	if err != nil {
		return fmt.Errorf("could not parse spec: %v", err)
	}

	rom, err := n64rom.NewBlankRomFile(byte(*filldata))
	if err != nil {
		return fmt.Errorf("n64rom.NewBlankRomFile: %v", err)
	}
	for _, w := range spec.Waves {
		for _, seg := range w.RawSegments {
			for _, include := range seg.Includes {
				f, err := os.Open(include)
				if err != nil {
					return fmt.Errorf("could not open include: %v", err)
				}
				spicy.CreateRawObjectWrapper(f, include+".o", ld)
			}
		}
		entry, err := spicy.CreateEntryBinary(w, as)
		if err != nil {
			return fmt.Errorf("spicy.CreateEntryBinary: %v", err)
		}
		linked_object, err := spicy.LinkSpec(w, ld, entry)
		if err != nil {
			return fmt.Errorf("spicy.LinkSpec: %v", err)
		}
		binarized_object, err := spicy.BinarizeObject(linked_object, objcopy)
		if err != nil {
			return fmt.Errorf("spicy.BinarizeObject: %v", err)
		}

		binarized_object_bytes, err := ioutil.ReadAll(binarized_object)
		if err != nil {
			return fmt.Errorf("could not read binarized object: %v", err)
		}
		rom.WriteAt(binarized_object_bytes, n64rom.CodeStart)
		if err != nil {
			return fmt.Errorf("could not write ROM: %v", err)
		}
	}
	out, err := os.Create(*rom_image_file)
	if err != nil {
		return fmt.Errorf("could not create ROM: %v", err)
	}
	// Pad the rom if necessary.
	if *romsize_mbits > 0 {
		minSize := int64(1000000 * *romsize_mbits / 8)
		_, err := out.WriteAt([]byte{0}, minSize)
		if err != nil {
			return err
		}
	}
	_, err = rom.Save(out)
	if err != nil {
		return fmt.Errorf("could not write ROM: %v", err)
	}
	return out.Close()
}

func main() {
	if err := mainE(); err != nil {
		log.Errorln("Error:", err)
		os.Exit(1)
	}
}
