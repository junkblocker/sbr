// Package processor handles parsing SMS/MMS backup XML files and extracting attachments.
package processor

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/junkblocker/sbr/types"
)

// workItem carries all pre-computed data a worker needs to save one attachment.
// Using a struct avoids separate goroutine argument copies.
type workItem struct {
	part       mmsPart
	datePrefix string
	sentTime   time.Time
	outPath    string
	// partIndex is the 0-based index of this part within its MMS message.
	// It is appended to the filename when the part has no explicit name, so
	// that multiple unnamed parts of the same type in one message each get a
	// distinct, stable output path instead of all colliding on the same name.
	partIndex int
	// disambigHash is non-empty when this part's natural (datePrefix, leafName)
	// key was already claimed by a different MMS element within the same file.
	// It holds the first 8 hex digits of the SHA-256 of the raw attachment
	// bytes and is injected into the filename, making the path both unique and
	// stable across full and incremental backup files: the same image bytes
	// always produce the same hash regardless of which file they come from or
	// what position in the file the MMS element occupies.
	disambigHash string
}

// ---------------------------------------------------------------------------
// Lean internal decode types - only the fields actually used for extraction.
// The exported types.MMS / types.MMSPart are kept for external callers.
// ---------------------------------------------------------------------------

// mmsPart is the minimal representation of an MMS <part> element needed to
// decide whether to save it and to derive the output filename.
type mmsPart struct {
	Data        string `xml:"data,attr"`
	ContentType string `xml:"ct,attr"`
	Filename    string `xml:"cl,attr"` // "Content-Location" maps to filename
	Name        string `xml:"name,attr"`
}

// mmsRecord is the minimal representation of an <mms> element.
// Skipping ReadableDate, ContactName, Addresses, Body, FromAddress, etc.
// reduces per-MMS allocation significantly on large backups.
type mmsRecord struct {
	Date  string    `xml:"date,attr"`
	Parts []mmsPart `xml:"parts>part"`
}

// windowsReservedNames is the set of base names (without extension) that are
// device names on Windows and therefore illegal as filenames on FAT32, exFAT,
// and NTFS - all filesystems commonly used on Android external storage and
// Windows sync targets.
var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true,
	"COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true,
	"LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// sanitiseLeafName makes a string safe to use as a filename component across
// Linux, macOS, Windows, and Android (FAT32 / exFAT / NTFS) filesystems:
//
//   - Control characters (\x00-\x1f) → '_'
//   - Path separators ('/' and '\') → '_'  (prevent accidental subdirectories)
//   - Windows-illegal characters (':', '*', '?', '"', '<', '>', '|') → '_'
//   - Trailing dots and spaces stripped  (FAT32/NTFS silently remove them,
//     which can create duplicate filenames on those filesystems)
//   - Leading dots stripped              (hidden-file semantics on Unix)
//   - Windows reserved device names (CON, NUL, COM1 ...) prefixed with '_'
//   - Length capped at 180 Unicode code points so that the full filename
//     (datePrefix + optional disambigHash + leaf) stays within the 255-byte
//     limit on most filesystems even for multi-byte UTF-8 strings
func sanitiseLeafName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < 0x20, r == '/', r == '\\', r == ':',
			r == '*', r == '?', r == '"', r == '<', r == '>', r == '|':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}

	// Strip trailing dots and spaces (FAT32/NTFS strip them silently).
	out := strings.TrimRight(b.String(), ". ")
	// Strip leading dots (hidden-file semantics on Unix).
	out = strings.TrimLeft(out, ".")

	// Cap at 180 code points (not bytes) to avoid splitting multi-byte runes.
	if utf8.RuneCountInString(out) > 180 {
		runes := []rune(out)
		out = string(runes[:180])
	}

	// Guard Windows reserved device names (case-insensitive, with or without
	// extension). Prefix with '_' to preserve recognisability.
	base := strings.ToUpper(strings.SplitN(out, ".", 2)[0])
	if windowsReservedNames[base] {
		out = "_" + out
	}

	return out
}

// buildFilenameInternal derives the output filename for one MMS part.
//
// Priority for the leaf name:
//  1. cl attribute (Content-Location), if non-empty and not "null"
//  2. name attribute, if non-empty and not "null"
//  3. Synthesised from partIndex + content-type extension
//
// The leaf name is passed through sanitiseLeafName before use so that URL
// values in the cl attribute (which contain slashes) never create accidental
// subdirectory paths in the output filename.
//
// disambigHash is empty for the common case (no collision). When non-empty it
// is the first 8 hex characters of the SHA-256 of the attachment bytes and is
// injected before the leaf name, producing a filename that is both unique and
// stable across full and incremental backup files: the same image always hashes
// to the same prefix, so the disambiguated name is idempotent across runs.
func buildFilenameInternal(part mmsPart, datePrefix string, partIndex int, disambigHash string) string {
	var leafName string
	if part.Filename != "" && part.Filename != "null" {
		leafName = sanitiseLeafName(part.Filename)
	} else if part.Name != "" && part.Name != "null" {
		leafName = sanitiseLeafName(part.Name)
	}

	if leafName == "" || leafName == "null" {
		// No usable name - always include partIndex for uniqueness within the message.
		if disambigHash != "" {
			return fmt.Sprintf("%s-%s-%d%s", datePrefix, disambigHash, partIndex, ExtForContentType(strings.ToLower(part.ContentType)))
		}
		return fmt.Sprintf("%s-%d%s", datePrefix, partIndex, ExtForContentType(strings.ToLower(part.ContentType)))
	}

	if disambigHash != "" {
		return fmt.Sprintf("%s-%s-%s", datePrefix, disambigHash, leafName)
	}
	return datePrefix + "-" + leafName
}

// contentHash returns the first 8 hex characters of the SHA-256 of raw
// (already base64-decoded) attachment bytes. Used as a stable disambiguator
// when two MMS messages would otherwise produce the same output filename.
func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:4]) // 4 bytes = 8 hex chars
}

// Options controls processor behaviour.
type Options struct {
	DebugLevel uint
}

// ExtForContentType returns the file extension for a given MIME content type.
// The content type should already be lowercased.
func ExtForContentType(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/heic":
		return ".heic"
	case "video/mp4":
		return ".mp4"
	case "video/3gpp":
		return ".3gp"
	case "audio/mpeg":
		return ".mp3"
	case "audio/amr":
		return ".amr"
	case "audio/vnd.qcelp":
		return ".qcp"
	case "text/plain":
		return ".txt"
	case "application/pdf":
		return ".pdf"
	case "text/x-vcard", "text/v-card", "text/vcard":
		return ".vcf"
	default:
		return ".bin"
	}
}

// BuildFilename constructs the output filename for an MMS attachment.
// datePrefix is a formatted timestamp string (e.g. "2006-01-02-150405").
// The content type used for extension lookup should be the original (un-lowercased) value
// from the part; this function lowercases it internally.
func BuildFilename(part types.MMSPart, datePrefix string) string {
	filename := ""
	if part.Filename != "" && part.Filename != "null" {
		filename = part.Filename
	} else if part.Name != "" && part.Name != "null" {
		filename = part.Name
	}

	if filename == "" || filename == "null" {
		ext := ExtForContentType(strings.ToLower(part.ContentType))
		return datePrefix + ext
	}
	return datePrefix + "-" + filename
}

// DatePrefixFromMillis converts a Unix millisecond timestamp string into a
// filename-safe date prefix of the form "2006-01-02-150405".
func DatePrefixFromMillis(dateMillis string) (string, time.Time, error) {
	timestamp, err := strconv.ParseInt(dateMillis, 10, 64)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("parsing date %q: %w", dateMillis, err)
	}
	sentTime := time.Unix(timestamp/1000, 0)
	return sentTime.Format("2006-01-02-150405"), sentTime, nil
}

// saveAttachment is the core write routine that operates on the lean mmsPart
// type and pre-computed time values (so the date string is not re-parsed per
// part). It is called by both the internal goroutines and the public API.
// partIndex is the 0-based index of the part within its parent MMS message and
// is used to disambiguate unnamed parts that share a content type.
func saveAttachment(part mmsPart, datePrefix string, sentTime time.Time, outPath string, partIndex int, disambigHash string, opts Options) error {
	filename := buildFilenameInternal(part, datePrefix, partIndex, disambigHash)
	oFile := filepath.Join(outPath, filename)

	// Stat first - on incremental runs almost every file already exists and we
	// want to skip the base64 decode and all subsequent work.
	oStat, err := os.Stat(oFile)
	if err == nil {
		if oStat.IsDir() {
			return fmt.Errorf("output path %s is an existing directory", oFile)
		}
		if opts.DebugLevel > 1 {
			fmt.Printf("DEBUG: Output path %s already exists\n", oFile)
		}
		return nil
	}

	data, err := base64.StdEncoding.DecodeString(part.Data)
	if err != nil {
		return fmt.Errorf("decoding attachment data: %w", err)
	}

	// Create a uniquely-named temp file in the same directory as the target so
	// that os.Rename is always an atomic same-filesystem move. Using a unique
	// name (rather than oFile+".tmp") means two concurrent goroutines writing
	// to the same final path - e.g. two MMS messages that share a timestamp
	// and filename - never stomp each other's temp file. The rename is
	// last-writer-wins, which is safe because both goroutines hold identical
	// content (truly duplicate attachments decode to the same bytes).
	tmp, err := os.CreateTemp(outPath, ".sbr-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", outPath, err)
	}
	oTempfile := tmp.Name()

	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(oTempfile)
		return fmt.Errorf("writing attachment to %s: %w", oTempfile, err)
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(oTempfile)
		return fmt.Errorf("closing temp file %s: %w", oTempfile, err)
	}

	if err = os.Chtimes(oTempfile, sentTime, sentTime); err != nil {
		_ = os.Remove(oTempfile)
		return fmt.Errorf("setting file time on %s: %w", oTempfile, err)
	}

	if err = os.Rename(oTempfile, oFile); err != nil {
		_ = os.Remove(oTempfile)
		return fmt.Errorf("renaming %s to %s: %w", oTempfile, oFile, err)
	}
	return nil
}

// naturalFilenameKey returns the collision-detection key for a part: the
// case-folded form of the filename that buildFilenameInternal would produce
// with no disambigHash.
//
// Case-folding is required because the output directory may live on a
// case-insensitive filesystem (NTFS, FAT32, exFAT - used on Windows and
// Android external storage): two filenames that differ only by case would
// collide on those filesystems even though they look distinct on Linux.
// Using a case-folded key ensures the disambigHash is injected whenever two
// parts would produce names that are case-insensitively identical, regardless
// of the host OS running the tool.
func naturalFilenameKey(part mmsPart, datePrefix string, partIndex int) string {
	return strings.ToLower(buildFilenameInternal(part, datePrefix, partIndex, ""))
}

// SaveMMSAttachment is the public API that decodes and writes a single MMS
// part. It accepts the exported types.MMSPart and a raw millisecond date
// string, converting them into the internal representation before delegating.
// partIndex is the 0-based index of this part within its parent MMS message;
// pass the actual index when saving multiple parts from the same message so
// that unnamed parts of the same content type receive distinct filenames.
// mmsIndex and disambig follow the same semantics as buildFilenameInternal;
// pass disambig=false when the caller has verified no cross-message collision.
func SaveMMSAttachment(part types.MMSPart, date, outPath string, partIndex int, opts Options) error {
	datePrefix, sentTime, err := DatePrefixFromMillis(date)
	if err != nil {
		return err
	}
	return saveAttachment(mmsPart{
		Data:        part.Data,
		ContentType: part.ContentType,
		Filename:    part.Filename,
		Name:        part.Name,
	}, datePrefix, sentTime, outPath, partIndex, "", opts)
}

// ProcessFile parses a single SMS/MMS backup XML file and saves attachments
// via a bounded worker pool. The pool size is 2×GOMAXPROCS; a buffered channel
// decouples the XML parser (producer) from I/O workers (consumers), keeping
// both busy without spawning one goroutine per attachment.
//
// ProcessFile blocks until all worker goroutines have finished writing. It does
// not touch the caller's WaitGroup at all - that is the caller's responsibility.
// This avoids the TOCTOU race where wg.Wait() in the caller could observe a zero
// count between the outer goroutine's Done() and ProcessFile's own wg.Add(1).
func ProcessFile(r io.Reader, filePath, outPath string, opts Options) {
	if opts.DebugLevel > 0 {
		fmt.Printf("Processing file: %s\n", filePath)
	}

	// Spin up the bounded worker pool.
	nWorkers := 2 * runtime.GOMAXPROCS(0)
	ch := make(chan workItem, nWorkers*4)

	var poolWg sync.WaitGroup
	poolWg.Add(nWorkers)
	for range nWorkers {
		go func() {
			defer poolWg.Done()
			for item := range ch {
				if saveErr := saveAttachment(item.part, item.datePrefix, item.sentTime, item.outPath, item.partIndex, item.disambigHash, opts); saveErr != nil {
					fmt.Println("Error saving attachment:", saveErr)
				}
			}
		}()
	}

	// seenKeys tracks every natural filename key assigned so far in this file.
	// The XML parser is single-threaded so no locking is needed. When a key is
	// seen for the second time (a different MMS element that would produce the
	// same natural filename), we compute a content hash and inject it into the
	// output path. The hash is derived from the attachment bytes themselves, so
	// it is identical whether the same message appears in an incremental or a
	// full backup - guaranteeing idempotent, deterministic filenames across the
	// entire full+incremental backup set.
	seenKeys := make(map[string]bool)

	decoder := xml.NewDecoder(r)

	parse:
	for {
		token, err := decoder.Token()
		if err != nil {
			if token != nil {
				fmt.Printf("Error decoding file %s: %s\n", filePath, err)
			}
			break
		} else if opts.DebugLevel > 2 {
			fmt.Printf("Decoded a token\n")
		}

		switch se := token.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "calls":
				fmt.Println("Not an SMS backup:", filePath)
				break parse
			case "sms":
				// SMS elements carry no attachments; skip without allocating.
				if err = decoder.Skip(); err != nil {
					fmt.Println("Error skipping SMS:", err)
				}
			case "mms":
				var mms mmsRecord
				if err = decoder.DecodeElement(&mms, &se); err != nil {
					fmt.Println("Error decoding MMS:", err)
					continue
				}
				// Hoist timestamp parsing outside the parts loop - all parts
				// of one MMS share the same date.
				datePrefix, sentTime, dateErr := DatePrefixFromMillis(mms.Date)
				if dateErr != nil {
					fmt.Println("Error parsing MMS date:", dateErr)
					continue
				}
				for i, part := range mms.Parts {
					contentType := strings.ToLower(part.ContentType)
					if isSupportedAttachment(contentType) {
						// Compute the natural key to detect cross-MMS collisions.
						naturalKey := naturalFilenameKey(part, datePrefix, i)
						collision := seenKeys[naturalKey]
						seenKeys[naturalKey] = true

						var disambigHash string
						if collision {
							// Eagerly decode to get the content hash. This is
							// the rare path; the common case (no collision) pays
							// no decode cost here.
							raw, decErr := base64.StdEncoding.DecodeString(part.Data)
							if decErr != nil {
								fmt.Printf("Error decoding attachment for hash (%s): %v\n", naturalKey, decErr)
								// Fall through with empty hash; saveAttachment
								// will catch the decode error again and report it.
							} else {
								disambigHash = contentHash(raw)
							}
						}

						ch <- workItem{
							part:         part,
							datePrefix:   datePrefix,
							sentTime:     sentTime,
							outPath:      outPath,
							partIndex:    i,
							disambigHash: disambigHash,
						}
					} else if contentType == "application/vnd.gsma.botmessage.v1.0+json" {
						if opts.DebugLevel > 2 {
							decoded, decErr := base64.StdEncoding.DecodeString(part.Data)
							if decErr != nil {
								fmt.Println("DEBUG: Error decoding attachment data:", decErr)
							} else {
								fmt.Printf("DEBUG: Data:\n%s\n", decoded)
							}
						}
					} else if contentType != "text/plain" && contentType != "application/smil" {
						fmt.Printf("  Unknown: %s\n", part.ContentType)
					}
				}
			}
		}
	}

	// Signal workers to drain, then wait for all writes to complete before
	// returning so the caller's WaitGroup.Done() is not called prematurely.
	close(ch)
	poolWg.Wait()
}

// isSupportedAttachment reports whether a (lowercased) content type should be
// saved as a file attachment.
func isSupportedAttachment(ct string) bool {
	return strings.HasPrefix(ct, "image/") ||
		strings.HasPrefix(ct, "audio/") ||
		strings.HasPrefix(ct, "video/") ||
		ct == "application/pdf" ||
		ct == "text/x-vcard" ||
		ct == "text/v-card" ||
		ct == "text/vcard" ||
		ct == "application/octet-stream"
}

// ProcessFileFromPath opens filePath and calls ProcessFile. It blocks until all
// attachments from the file have been written to disk.
func ProcessFileFromPath(filePath, outPath string, opts Options) {
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Printf("Error opening file %s: %s\n", filePath, err)
		return
	}
	defer file.Close()
	ProcessFile(file, filePath, outPath, opts)
}

// ProcessDirectory walks inDirPath and processes every file matching the
// "sms-*.xml" naming convention. Files are processed concurrently - each is
// opened and parsed in its own goroutine. Because ProcessFile now blocks until
// its worker pool has drained, each goroutine here holds exactly one wg count
// for the entire duration of its file processing, and wg.Wait() in main()
// correctly waits for all writes to complete with no internal wg.Add races.
func ProcessDirectory(wg *sync.WaitGroup, inDirPath, outDirPath string, opts Options) {
	err := filepath.WalkDir(inDirPath, func(apath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fname := entry.Name()
		if !entry.IsDir() {
			if strings.HasPrefix(fname, "sms-") && strings.HasSuffix(fname, ".xml") {
				// wg.Add before launching so wg.Wait() in the caller cannot
				// return before this goroutine has started and ProcessFile has
				// completed all its writes.
				wg.Add(1)
				go func(path string) {
					defer wg.Done()
					ProcessFileFromPath(path, outDirPath, opts)
				}(apath)
			} else if opts.DebugLevel > 1 {
				fmt.Println("DEBUG: Skipping", entry)
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("Failed to walk directory %s: %v\n", inDirPath, err)
	}
}
