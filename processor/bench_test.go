package processor

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/junkblocker/sbr/types"
)

// ---------------------------------------------------------------------------
// Synthetic XML generators
// ---------------------------------------------------------------------------

// fakeJPEG is a 100-byte payload that stands in for a real image.
var fakeJPEG = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xff, 0xd8, 0x00, 0x01}, 25))

// buildXML creates a synthetic backup with nSMS plain-text messages and
// nMMS messages each carrying one image attachment.
func buildXML(nSMS, nMMS int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>`+"\n")
	fmt.Fprintf(&b, `<smses count="%d">`+"\n", nSMS+nMMS)

	for i := range nSMS {
		fmt.Fprintf(&b,
			`  <sms address="+1555%07d" date="1705318245000" body="msg %d" type="1" read="1" status="-1" locked="0" date_sent="0" readable_date="" contact_name="" seen="1"/>`+"\n",
			i, i)
	}
	for i := range nMMS {
		ts := int64(1705318245000) + int64(i)*1000
		fmt.Fprintf(&b,
			`  <mms date="%d" address="+1555%07d" contact_name="" readable_date="" m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0" from_address="~" seen="1">`+"\n",
			ts, i)
		fmt.Fprintf(&b, `    <parts>`+"\n")
		fmt.Fprintf(&b,
			`      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null" cid="" cl="img%d.jpg" text="null" data="%s"/>`+"\n",
			i, fakeJPEG)
		fmt.Fprintf(&b, `    </parts>`+"\n")
		fmt.Fprintf(&b, `    <addrs/>`+"\n")
		fmt.Fprintf(&b, `  </mms>`+"\n")
	}
	fmt.Fprintf(&b, `</smses>`)
	return []byte(b.String())
}

// ---------------------------------------------------------------------------
// Benchmarks: baseline (all files new)
// ---------------------------------------------------------------------------

// BenchmarkProcessFile_AllNew measures parsing + writing for a file where
// none of the output files exist yet (first-run / full-backup scenario).
func BenchmarkProcessFile_AllNew(b *testing.B) {
	for _, tc := range []struct {
		name string
		sms  int
		mms  int
	}{
		{"10k_sms_1k_mms", 10_000, 1_000},
		{"50k_sms_5k_mms", 50_000, 5_000},
	} {
		b.Run(tc.name, func(b *testing.B) {
			xml := buildXML(tc.sms, tc.mms)
			b.ResetTimer()
			for range b.N {
				dir := b.TempDir()
				ProcessFile(bytes.NewReader(xml), "bench.xml", dir, Options{})
			}
		})
	}
}

// BenchmarkProcessFile_AllExisting measures the incremental "skip everything"
// path — the dominant case on daily runs against a large backup.
func BenchmarkProcessFile_AllExisting(b *testing.B) {
	const nMMS = 1_000
	xml := buildXML(0, nMMS)

	// Pre-populate the output dir once, outside the benchmark loop.
	baseDir := b.TempDir()
	ProcessFile(bytes.NewReader(xml), "seed.xml", baseDir, Options{})

	b.ResetTimer()
	for range b.N {
		// Each iteration re-reads the same XML against an already-full dir.
		ProcessFile(bytes.NewReader(xml), "bench.xml", baseDir, Options{})
	}
}

// ---------------------------------------------------------------------------
// Micro-benchmarks for the individual hot functions
// ---------------------------------------------------------------------------

func BenchmarkSaveMMSAttachment_New(b *testing.B) {
	dir := b.TempDir()
	part := types.MMSPart{
		Data:        fakeJPEG,
		ContentType: "image/jpeg",
		Filename:    "photo.jpg",
	}
	b.ResetTimer()
	for i := range b.N {
		// Use a unique date so we never hit the "already exists" branch.
		date := fmt.Sprintf("%d", 1705318245000+int64(i)*1000)
		_ = SaveMMSAttachment(part, date, dir, 0, Options{})
	}
}

func BenchmarkSaveMMSAttachment_Existing(b *testing.B) {
	dir := b.TempDir()
	part := types.MMSPart{
		Data:        fakeJPEG,
		ContentType: "image/jpeg",
		Filename:    "photo.jpg",
	}
	// Seed the file.
	_ = SaveMMSAttachment(part, "1705318245000", dir, 0, Options{})

	b.ResetTimer()
	for range b.N {
		// Same date every time → always hits "already exists".
		_ = SaveMMSAttachment(part, "1705318245000", dir, 0, Options{})
	}
}

func BenchmarkBuildFilename(b *testing.B) {
	part := types.MMSPart{ContentType: "image/jpeg", Filename: "photo.jpg"}
	b.ResetTimer()
	for range b.N {
		_ = BuildFilename(part, "2024-01-15-113045")
	}
}

func BenchmarkDatePrefixFromMillis(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		_, _, _ = DatePrefixFromMillis("1705318245000")
	}
}

func BenchmarkExtForContentType(b *testing.B) {
	types := []string{"image/jpeg", "video/mp4", "audio/mpeg", "application/pdf", "text/vcard"}
	b.ResetTimer()
	for i := range b.N {
		_ = ExtForContentType(types[i%len(types)])
	}
}

// BenchmarkXMLDecode_SMSOnly measures pure XML parse cost for SMS-only records
// (no attachments — exercises the skip-MMS path and string allocation).
func BenchmarkXMLDecode_SMSOnly(b *testing.B) {
	xml := buildXML(10_000, 0)
	b.ResetTimer()
	for range b.N {
		dir := b.TempDir()
		ProcessFile(bytes.NewReader(xml), "bench.xml", dir, Options{})
	}
}

// BenchmarkOutputDirStat measures the cost of os.Stat on an existing file —
// the key syscall in the incremental skip path.
func BenchmarkOutputDirStat(b *testing.B) {
	dir := b.TempDir()
	f, _ := os.CreateTemp(dir, "bench")
	f.Close()
	name := f.Name()
	b.ResetTimer()
	for range b.N {
		_, _ = os.Stat(name)
	}
}

// BenchmarkProcessDirectory_Concurrent simulates the real workload:
// one large file (50k SMS + 5k MMS) alongside 6 small incremental files
// (500 SMS + 100 MMS each), all run via ProcessDirectory with concurrent
// file processing. Output dir is pre-populated so the dominant path is skips.
func BenchmarkProcessDirectory_Concurrent(b *testing.B) {
	largeXML := buildXML(50_000, 5_000)
	smallXML := buildXML(500, 100)

	// Seed the output dir with all attachments so we exercise the skip path.
	outDir := b.TempDir()
	{
		ProcessFile(bytes.NewReader(largeXML), "seed-large.xml", outDir, Options{})
		for i := range 6 {
			ProcessFile(bytes.NewReader(smallXML), fmt.Sprintf("seed-small-%d.xml", i), outDir, Options{})
		}
	}

	// Write the input files to a temp directory so ProcessDirectory can walk them.
	inDir := b.TempDir()
	if err := os.WriteFile(filepath.Join(inDir, "sms-large.xml"), largeXML, 0644); err != nil {
		b.Fatal(err)
	}
	for i := range 6 {
		name := fmt.Sprintf("sms-small-%d.xml", i)
		if err := os.WriteFile(filepath.Join(inDir, name), smallXML, 0644); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for range b.N {
		var wg sync.WaitGroup
		ProcessDirectory(&wg, inDir, outDir, Options{})
		wg.Wait()
	}
}
