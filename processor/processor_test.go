package processor

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/junkblocker/sbr/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustEncode(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// pfx computes the filename date prefix for a millisecond timestamp string.
// It panics on error (safe for use in package-level var initialisers).
func pfx(ms string) string {
	p, _, err := DatePrefixFromMillis(ms)
	if err != nil {
		panic(err)
	}
	return p
}

// Precomputed date prefixes for the two timestamps used most often in tests.
// Using package-level vars ensures they match whatever timezone the test binary
// runs in, instead of hardcoding UTC-derived strings.
var (
	// ts1 = 1705318245000 ms (2024-01-15 11:30:45 UTC)
	ts1Prefix = pfx("1705318245000")
	// ts2 = 1705318305000 ms (60 s later)
	ts2Prefix = pfx("1705318305000")
)

// readDir returns the sorted names of regular files in dir.
func readDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// assertFile asserts that path exists and contains exactly wantContent.
func assertFile(t *testing.T, path string, wantContent []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected file %q: %v", path, err)
	}
	if string(got) != string(wantContent) {
		t.Errorf("file %q: content = %q, want %q", path, got, wantContent)
	}
}

// ---------------------------------------------------------------------------
// ExtForContentType
// ---------------------------------------------------------------------------

func TestExtForContentType(t *testing.T) {
	cases := []struct {
		ct   string
		want string
	}{
		{"image/jpeg", ".jpg"},
		{"image/png", ".png"},
		{"image/gif", ".gif"},
		{"image/heic", ".heic"},
		{"video/mp4", ".mp4"},
		{"video/3gpp", ".3gp"},
		{"audio/mpeg", ".mp3"},
		{"audio/amr", ".amr"},
		{"audio/vnd.qcelp", ".qcp"},
		{"text/plain", ".txt"},
		{"application/pdf", ".pdf"},
		{"text/x-vcard", ".vcf"},
		{"text/v-card", ".vcf"},
		{"text/vcard", ".vcf"},
		// Unknown type falls back to .bin
		{"application/octet-stream", ".bin"},
		{"totally/unknown", ".bin"},
		{"", ".bin"},
		// Uppercase should NOT match (caller must lowercase first)
		{"image/JPEG", ".bin"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.ct, func(t *testing.T) {
			got := ExtForContentType(tc.ct)
			if got != tc.want {
				t.Errorf("ExtForContentType(%q) = %q, want %q", tc.ct, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DatePrefixFromMillis
// ---------------------------------------------------------------------------

func TestDatePrefixFromMillis(t *testing.T) {
	t.Run("valid timestamp", func(t *testing.T) {
		const ms = "1705318245000"
		prefix, sentTime, err := DatePrefixFromMillis(ms)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Expected prefix is local-time formatted - derive it the same way.
		wantTime := time.Unix(1705318245, 0)
		want := wantTime.Format("2006-01-02-150405")
		if prefix != want {
			t.Errorf("prefix = %q, want %q", prefix, want)
		}
		if !sentTime.Equal(wantTime) {
			t.Errorf("sentTime = %v, want %v", sentTime, wantTime)
		}
	})

	t.Run("timestamp truncated to seconds", func(t *testing.T) {
		// Millisecond remainder should be dropped (integer division).
		// Both 1705318245000 and 1705318245999 should produce the same prefix.
		p1, _, err1 := DatePrefixFromMillis("1705318245000")
		p2, _, err2 := DatePrefixFromMillis("1705318245999")
		if err1 != nil || err2 != nil {
			t.Fatalf("unexpected error: %v %v", err1, err2)
		}
		if p1 != p2 {
			t.Errorf("prefix mismatch: %q vs %q (sub-second part not stripped)", p1, p2)
		}
	})

	t.Run("zero timestamp", func(t *testing.T) {
		prefix, sentTime, err := DatePrefixFromMillis("0")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantTime := time.Unix(0, 0).UTC()
		if !sentTime.Equal(wantTime) {
			t.Errorf("sentTime = %v, want %v", sentTime, wantTime)
		}
		if prefix == "" {
			t.Error("prefix should not be empty for zero timestamp")
		}
	})

	t.Run("invalid string", func(t *testing.T) {
		_, _, err := DatePrefixFromMillis("not-a-number")
		if err == nil {
			t.Error("expected error for non-numeric input, got nil")
		}
	})

	t.Run("empty string", func(t *testing.T) {
		_, _, err := DatePrefixFromMillis("")
		if err == nil {
			t.Error("expected error for empty string, got nil")
		}
	})

	t.Run("negative timestamp", func(t *testing.T) {
		// Negative ms timestamps (pre-epoch) should be accepted by strconv but
		// produce a date before 1970.
		prefix, sentTime, err := DatePrefixFromMillis("-1000")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantTime := time.Unix(-1, 0).UTC()
		if !sentTime.Equal(wantTime) {
			t.Errorf("sentTime = %v, want %v", sentTime, wantTime)
		}
		if prefix == "" {
			t.Error("prefix should not be empty for negative timestamp")
		}
	})
}

// ---------------------------------------------------------------------------
// BuildFilename
// ---------------------------------------------------------------------------

func TestBuildFilename(t *testing.T) {
	prefix := ts1Prefix

	t.Run("uses Filename when set", func(t *testing.T) {
		part := types.MMSPart{Filename: "photo.jpg", ContentType: "image/jpeg"}
		got := BuildFilename(part, prefix)
		want := prefix + "-photo.jpg"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to Name when Filename empty", func(t *testing.T) {
		part := types.MMSPart{Filename: "", Name: "audio.mp3", ContentType: "audio/mpeg"}
		got := BuildFilename(part, prefix)
		want := prefix + "-audio.mp3"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to Name when Filename is null literal", func(t *testing.T) {
		part := types.MMSPart{Filename: "null", Name: "voice.amr", ContentType: "audio/amr"}
		got := BuildFilename(part, prefix)
		want := prefix + "-voice.amr"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("generates extension when both Filename and Name are empty", func(t *testing.T) {
		part := types.MMSPart{Filename: "", Name: "", ContentType: "image/png"}
		got := BuildFilename(part, prefix)
		want := prefix + ".png"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("generates extension when both are null literal", func(t *testing.T) {
		part := types.MMSPart{Filename: "null", Name: "null", ContentType: "video/mp4"}
		got := BuildFilename(part, prefix)
		want := prefix + ".mp4"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("unknown content type gets .bin extension", func(t *testing.T) {
		part := types.MMSPart{Filename: "", Name: "", ContentType: "application/x-custom"}
		got := BuildFilename(part, prefix)
		want := prefix + ".bin"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("content type is lowercased for extension lookup", func(t *testing.T) {
		part := types.MMSPart{Filename: "", Name: "", ContentType: "image/JPEG"}
		got := BuildFilename(part, prefix)
		want := prefix + ".jpg"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("vcard content type variant", func(t *testing.T) {
		for _, ct := range []string{"text/x-vcard", "text/v-card", "text/vcard", "text/x-vCard", "text/V-Card"} {
			part := types.MMSPart{Filename: "", Name: "", ContentType: ct}
			got := BuildFilename(part, prefix)
			want := prefix + ".vcf"
			if got != want {
				t.Errorf("ct=%q: got %q, want %q", ct, got, want)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// sanitiseLeafName
// ---------------------------------------------------------------------------

func TestSanitiseLeafName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Plain filename - unchanged.
		{"photo.jpg", "photo.jpg"},
		// URL used as cl attribute - slashes AND colon become underscores.
		{
			"https:/rbm-us.storage.googleapis.com/89762386408/mi3f2rp2MqPAosayygFXtH8P",
			"https__rbm-us.storage.googleapis.com_89762386408_mi3f2rp2MqPAosayygFXtH8P",
		},
		// All Windows-illegal characters replaced.
		{`a:b*c?d"e<f>g|h`, "a_b_c_d_e_f_g_h"},
		// Backslash.
		{`foo\bar`, "foo_bar"},
		// Control characters.
		{"foo\x00bar\x1fend", "foo_bar_end"},
		// Leading dot removed.
		{".hidden", "hidden"},
		// Multiple leading dots.
		{"...dots", "dots"},
		// Empty string stays empty.
		{"", ""},
		// String that becomes empty after trimming dots.
		{"...", ""},
		// Trailing dots stripped (FAT32/NTFS silent removal).
		{"foo...", "foo"},
		// Trailing spaces stripped.
		{"foo   ", "foo"},
		// Trailing dot-space combination.
		{"foo. ", "foo"},
		// Windows reserved name without extension → prefixed with '_'.
		{"NUL", "_NUL"},
		// Windows reserved name with extension → prefixed with '_'.
		{"COM1.jpg", "_COM1.jpg"},
		// Windows reserved name case-insensitive → prefixed with '_'.
		{"con.txt", "_con.txt"},
		// Non-reserved name that starts like a reserved one - unchanged.
		{"NULLIFY.jpg", "NULLIFY.jpg"},
		// Long name is capped at 180 code points (not bytes).
		{strings.Repeat("a", 250), strings.Repeat("a", 180)},
		// Multi-byte runes: cap at 180 runes, not bytes.
		{strings.Repeat("á", 200), strings.Repeat("á", 180)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in[:min(len(tc.in), 40)], func(t *testing.T) {
			got := sanitiseLeafName(tc.in)
			if got != tc.want {
				t.Errorf("sanitiseLeafName(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// buildFilenameInternal / contentHash - disambiguation
// ---------------------------------------------------------------------------

func TestBuildFilenameInternal_PartIndex(t *testing.T) {
	prefix := ts1Prefix

	t.Run("named part with no hash", func(t *testing.T) {
		p := mmsPart{Filename: "pic.jpg", ContentType: "image/jpeg"}
		got := buildFilenameInternal(p, prefix, 3, "")
		want := prefix + "-pic.jpg"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("named part with hash injected", func(t *testing.T) {
		p := mmsPart{Filename: "pic.jpg", ContentType: "image/jpeg"}
		got := buildFilenameInternal(p, prefix, 0, "aabbccdd")
		want := prefix + "-aabbccdd-pic.jpg"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("unnamed parts get distinct names via partIndex (no hash)", func(t *testing.T) {
		p := mmsPart{Filename: "null", Name: "null", ContentType: "image/jpeg"}
		got0 := buildFilenameInternal(p, prefix, 0, "")
		got1 := buildFilenameInternal(p, prefix, 1, "")
		got2 := buildFilenameInternal(p, prefix, 2, "")
		if got0 == got1 || got1 == got2 || got0 == got2 {
			t.Errorf("unnamed parts produced colliding names: %q %q %q", got0, got1, got2)
		}
		for _, name := range []string{got0, got1, got2} {
			if !strings.HasSuffix(name, ".jpg") {
				t.Errorf("expected .jpg suffix, got %q", name)
			}
		}
	})

	t.Run("unnamed part with hash includes hash and partIndex", func(t *testing.T) {
		p := mmsPart{ContentType: "image/jpeg"}
		got := buildFilenameInternal(p, prefix, 1, "deadbeef")
		want := prefix + "-deadbeef-1.jpg"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("index zero unnamed no-hash matches expected pattern", func(t *testing.T) {
		p := mmsPart{ContentType: "image/png"}
		got := buildFilenameInternal(p, prefix, 0, "")
		want := prefix + "-0.png"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("index five unnamed no-hash matches expected pattern", func(t *testing.T) {
		p := mmsPart{ContentType: "video/mp4"}
		got := buildFilenameInternal(p, prefix, 5, "")
		want := prefix + "-5.mp4"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestContentHash(t *testing.T) {
	t.Run("same bytes produce same hash", func(t *testing.T) {
		data := []byte("some image content")
		h1 := contentHash(data)
		h2 := contentHash(data)
		if h1 != h2 {
			t.Errorf("same input produced different hashes: %q vs %q", h1, h2)
		}
	})

	t.Run("hash is 8 hex characters", func(t *testing.T) {
		h := contentHash([]byte("test"))
		if len(h) != 8 {
			t.Errorf("expected 8-char hash, got %q (len %d)", h, len(h))
		}
		for _, c := range h {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("hash %q contains non-hex character %q", h, c)
			}
		}
	})

	t.Run("different bytes produce different hashes", func(t *testing.T) {
		h1 := contentHash([]byte("image-A"))
		h2 := contentHash([]byte("image-B"))
		if h1 == h2 {
			t.Errorf("different inputs produced same hash: %q", h1)
		}
	})
}

// ---------------------------------------------------------------------------
// isSupportedAttachment
// ---------------------------------------------------------------------------

func TestIsSupportedAttachment(t *testing.T) {
	supported := []string{
		"image/jpeg", "image/png", "image/gif", "image/webp",
		"audio/mpeg", "audio/amr",
		"video/mp4", "video/3gpp",
		"application/pdf",
		"text/x-vcard", "text/v-card", "text/vcard",
		"application/octet-stream",
	}
	for _, ct := range supported {
		if !isSupportedAttachment(ct) {
			t.Errorf("isSupportedAttachment(%q) = false, want true", ct)
		}
	}

	unsupported := []string{
		"text/plain",
		"application/smil",
		"application/vnd.gsma.botmessage.v1.0+json",
		"",
	}
	for _, ct := range unsupported {
		if isSupportedAttachment(ct) {
			t.Errorf("isSupportedAttachment(%q) = true, want false", ct)
		}
	}
}

// ---------------------------------------------------------------------------
// SaveMMSAttachment
// ---------------------------------------------------------------------------

func TestSaveMMSAttachment(t *testing.T) {
	opts := Options{}

	t.Run("saves file with correct content and timestamp", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("fake image data")
		encoded := mustEncode(string(content))
		dateMs := "1705318245000"

		part := types.MMSPart{
			Data:        encoded,
			ContentType: "image/jpeg",
			Filename:    "test.jpg",
		}

		if err := SaveMMSAttachment(part, dateMs, dir, 0, opts); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := filepath.Join(dir, ts1Prefix+"-test.jpg")
		assertFile(t, expected, content)

		info, _ := os.Stat(expected)
		wantMtime := time.Unix(1705318245, 0)
		if !info.ModTime().Equal(wantMtime) {
			t.Errorf("mtime = %v, want %v", info.ModTime(), wantMtime)
		}
	})

	t.Run("generates filename from content type when no name (index 0)", func(t *testing.T) {
		dir := t.TempDir()
		part := types.MMSPart{Data: mustEncode("pdf data"), ContentType: "application/pdf"}
		if err := SaveMMSAttachment(part, "1705318245000", dir, 0, opts); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := filepath.Join(dir, ts1Prefix+"-0.pdf")
		if _, err := os.Stat(expected); os.IsNotExist(err) {
			t.Errorf("expected file %q not created", expected)
		}
	})

	t.Run("different partIndex gives different unnamed filename", func(t *testing.T) {
		dir := t.TempDir()
		part := types.MMSPart{Data: mustEncode("data"), ContentType: "image/jpeg"}

		if err := SaveMMSAttachment(part, "1705318245000", dir, 0, opts); err != nil {
			t.Fatalf("index 0: %v", err)
		}
		if err := SaveMMSAttachment(part, "1705318245000", dir, 1, opts); err != nil {
			t.Fatalf("index 1: %v", err)
		}

		names := readDir(t, dir)
		if len(names) != 2 {
			t.Fatalf("expected 2 files, got %d: %v", len(names), names)
		}
	})

	t.Run("skips existing file silently", func(t *testing.T) {
		dir := t.TempDir()
		expected := filepath.Join(dir, ts1Prefix+"-test.jpg")
		originalContent := []byte("original")
		if err := os.WriteFile(expected, originalContent, 0644); err != nil {
			t.Fatal(err)
		}

		part := types.MMSPart{
			Data:        mustEncode("new content"),
			ContentType: "image/jpeg",
			Filename:    "test.jpg",
		}
		if err := SaveMMSAttachment(part, "1705318245000", dir, 0, opts); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		assertFile(t, expected, originalContent)
	})

	t.Run("returns error for invalid base64", func(t *testing.T) {
		dir := t.TempDir()
		part := types.MMSPart{Data: "!!!not-valid-base64!!!", ContentType: "image/jpeg"}
		if err := SaveMMSAttachment(part, "1705318245000", dir, 0, opts); err == nil {
			t.Error("expected error for invalid base64, got nil")
		}
	})

	t.Run("returns error for invalid date", func(t *testing.T) {
		dir := t.TempDir()
		part := types.MMSPart{Data: mustEncode("data"), ContentType: "image/jpeg"}
		if err := SaveMMSAttachment(part, "not-a-number", dir, 0, opts); err == nil {
			t.Error("expected error for non-numeric date, got nil")
		}
	})

	t.Run("returns error when output path is an existing directory", func(t *testing.T) {
		dir := t.TempDir()
		conflictDir := filepath.Join(dir, ts1Prefix+"-test.jpg")
		if err := os.Mkdir(conflictDir, 0755); err != nil {
			t.Fatal(err)
		}
		part := types.MMSPart{
			Data:        mustEncode("data"),
			ContentType: "image/jpeg",
			Filename:    "test.jpg",
		}
		err := SaveMMSAttachment(part, "1705318245000", dir, 0, opts)
		if err == nil {
			t.Error("expected error when output path is a directory, got nil")
		}
		if !strings.Contains(err.Error(), "existing directory") {
			t.Errorf("error message %q should mention 'existing directory'", err.Error())
		}
	})

	t.Run("null filename falls back to content-type extension with index", func(t *testing.T) {
		dir := t.TempDir()
		part := types.MMSPart{
			Data:        mustEncode("gif"),
			ContentType: "image/gif",
			Filename:    "null",
			Name:        "null",
		}
		if err := SaveMMSAttachment(part, "1705318245000", dir, 2, opts); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := filepath.Join(dir, ts1Prefix+"-2.gif")
		if _, err := os.Stat(expected); os.IsNotExist(err) {
			t.Errorf("expected file %q not created", expected)
		}
	})

	t.Run("no leftover .tmp file on success", func(t *testing.T) {
		dir := t.TempDir()
		part := types.MMSPart{
			Data:        mustEncode("mp3"),
			ContentType: "audio/mpeg",
			Filename:    "song.mp3",
		}
		if err := SaveMMSAttachment(part, "1705318245000", dir, 0, opts); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// No .tmp files of any name should remain after a successful save.
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				t.Errorf("leftover .tmp file found: %q", e.Name())
			}
		}
	})
}

// ---------------------------------------------------------------------------
// ProcessFile (XML parsing)
// ---------------------------------------------------------------------------

func TestProcessFile_SMSBackup(t *testing.T) {
	t.Run("parses MMS with image attachment", func(t *testing.T) {
		dir := t.TempDir()
		imageData := mustEncode("fake jpeg")

		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="1705318245000" address="+15551234567" contact_name="Test User"
       readable_date="Jan 15, 2024 12:30:45 PM" m_cls="" m_size="" text_only="0"
       read="1" locked="0" date_sent="0" from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="&lt;image&gt;" cl="photo.jpg" ctt_s="null" ctt_t="null"
            text="null" data="` + imageData + `"/>
    </parts>
    <addrs>
      <addr address="+15551234567" type="137" charset="106"/>
    </addrs>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		expected := filepath.Join(dir, ts1Prefix+"-photo.jpg")
		if _, err := os.Stat(expected); os.IsNotExist(err) {
			t.Errorf("expected attachment %q not saved", expected)
		}
	})

	t.Run("skips text/plain and application/smil parts", func(t *testing.T) {
		dir := t.TempDir()
		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="1705318245000" address="+15551234567" contact_name="Test" readable_date=""
       m_cls="" m_size="" text_only="1" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="text/plain" name="null" chset="null" cd="null" fn="null"
            cid="" cl="null" text="hello world" data="null"/>
      <part seq="1" ct="application/smil" name="null" chset="null" cd="null" fn="null"
            cid="" cl="null" text="null" data="PHNtaWw+PC9zbWlsPg=="/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		if names := readDir(t, dir); len(names) != 0 {
			t.Errorf("expected no files saved for text-only MMS, got %v", names)
		}
	})

	t.Run("SMS elements are parsed without error", func(t *testing.T) {
		dir := t.TempDir()
		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <sms address="+15559876543" date="1705318245000" body="Hello!" type="1"
       read="1" status="-1" locked="0" date_sent="0" readable_date=""
       contact_name="Friend" seen="1"/>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		if names := readDir(t, dir); len(names) != 0 {
			t.Errorf("expected no files for SMS-only backup, got %v", names)
		}
	})

	t.Run("returns early for call log backup", func(t *testing.T) {
		dir := t.TempDir()
		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<calls count="1">
  <call number="+15551234567" duration="30" date="1705318245000" type="1"
        readable_date="" contact_name="Unknown"/>
</calls>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		if names := readDir(t, dir); len(names) != 0 {
			t.Errorf("expected no files for call backup, got %v", names)
		}
	})

	t.Run("handles multiple MMS messages", func(t *testing.T) {
		dir := t.TempDir()
		data1 := mustEncode("img1")
		data2 := mustEncode("img2")

		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="2">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="a.jpg" text="null" data="` + data1 + `"/>
    </parts>
    <addrs/>
  </mms>
  <mms date="1705318305000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/png" name="null" chset="null" cd="null" fn="null"
            cid="" cl="b.png" text="null" data="` + data2 + `"/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		for _, name := range []string{ts1Prefix + "-a.jpg", ts2Prefix + "-b.png"} {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); os.IsNotExist(err) {
				t.Errorf("expected file %q not found", p)
			}
		}
	})

	t.Run("handles empty smses element", func(t *testing.T) {
		dir := t.TempDir()
		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="0"/>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		if names := readDir(t, dir); len(names) != 0 {
			t.Errorf("expected no files for empty backup, got %v", names)
		}
	})

	t.Run("handles invalid XML gracefully", func(t *testing.T) {
		dir := t.TempDir()
		// Should not panic.
		ProcessFile(strings.NewReader(`<?xml version='1.0'?><smses><mms date="bad" unclosed`), "test.xml", dir, Options{})
	})

	t.Run("MMS with vcard attachment", func(t *testing.T) {
		dir := t.TempDir()
		vcardContent := "BEGIN:VCARD\nVERSION:3.0\nFN:Test\nEND:VCARD"
		encoded := mustEncode(vcardContent)

		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="text/x-vcard" name="null" chset="null" cd="null" fn="null"
            cid="" cl="contact.vcf" text="null" data="` + encoded + `"/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		expected := filepath.Join(dir, ts1Prefix+"-contact.vcf")
		if _, err := os.Stat(expected); os.IsNotExist(err) {
			t.Errorf("expected vcard file %q not saved", expected)
		}
	})

	t.Run("MMS with URL as cl attribute is saved without directory traversal error", func(t *testing.T) {
		// Real-world case: RCS/RBM messages use a full HTTPS URL as the
		// Content-Location (cl) attribute. Without sanitisation the slashes
		// cause os.Rename to fail with "no such file or directory" because
		// the OS interprets them as path separators.
		dir := t.TempDir()
		imgData := mustEncode("rcs-image-bytes")
		url := "https:/rbm-us.storage.googleapis.com/89762386408/mi3f2rp2MqPAosayygFXtH8P"

		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="` + url + `" text="null" data="` + imgData + `"/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		// Slashes and colons in the URL should have been replaced with underscores.
		sanitised := sanitiseLeafName(url)
		expected := filepath.Join(dir, ts1Prefix+"-"+sanitised)
		assertFile(t, expected, []byte("rcs-image-bytes"))

		// Crucially: no subdirectories should have been created.
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				t.Errorf("unexpected subdirectory created in output dir: %q", e.Name())
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Cross-MMS collision: same timestamp + same filename across two messages
// ---------------------------------------------------------------------------

// hashOf is a test helper that computes the expected disambigHash for a known
// byte slice, matching what the production code injects into filenames.
func hashOf(t *testing.T, data []byte) string {
	t.Helper()
	return contentHash(data)
}

// TestProcessFile_CrossMMSCollision covers the real-world failure mode where two
// separate MMS messages share the same second-precision timestamp AND the same
// attachment filename (e.g. both named "image000000.jpg" by the app).
//
// The fix operates in two layers:
//  1. The XML parser detects the collision before enqueueing and computes a
//     SHA-256 content hash for the colliding part. The hash is injected into
//     the filename, making the path unique AND stable across full and incremental
//     backup files: the same bytes always hash to the same prefix regardless of
//     which file or position the MMS element appears in.
//  2. The temp-file rename uses os.CreateTemp so concurrent goroutines writing
//     to the same final path never stomp each other's temp file.
func TestProcessFile_CrossMMSCollision(t *testing.T) {
	t.Run("same timestamp + same cl, identical content → natural + hash-qualified file", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("shared-image-content")
		sameData := mustEncode(string(content))
		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="2">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="image000000.jpg" text="null" data="` + sameData + `"/>
    </parts>
    <addrs/>
  </mms>
  <mms date="1705318245000" address="+2" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="image000000.jpg" text="null" data="` + sameData + `"/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		names := readDir(t, dir)
		for _, n := range names {
			if strings.HasSuffix(n, ".tmp") {
				t.Errorf("leftover temp file: %q", n)
			}
		}
		natural := ts1Prefix + "-image000000.jpg"
		qualified := fmt.Sprintf("%s-%s-image000000.jpg", ts1Prefix, hashOf(t, content))
		assertFile(t, filepath.Join(dir, natural), content)
		assertFile(t, filepath.Join(dir, qualified), content)
		if len(names) != 2 {
			t.Errorf("expected 2 output files, got %d: %v", len(names), names)
		}
	})

	t.Run("same timestamp + same cl, different content → both saved under distinct hash-qualified names", func(t *testing.T) {
		dir := t.TempDir()
		firstContent := []byte("first-image")
		secondContent := []byte("second-image")
		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="2">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="image000000.jpg" text="null" data="` + mustEncode(string(firstContent)) + `"/>
    </parts>
    <addrs/>
  </mms>
  <mms date="1705318245000" address="+2" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="image000000.jpg" text="null" data="` + mustEncode(string(secondContent)) + `"/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		natural := ts1Prefix + "-image000000.jpg"
		qualified := fmt.Sprintf("%s-%s-image000000.jpg", ts1Prefix, hashOf(t, secondContent))
		assertFile(t, filepath.Join(dir, natural), firstContent)
		assertFile(t, filepath.Join(dir, qualified), secondContent)

		if names := readDir(t, dir); len(names) != 2 {
			t.Errorf("expected 2 output files, got %d: %v", len(names), names)
		}
	})

	t.Run("three MMS with same timestamp + same cl each get a distinct hash-qualified name", func(t *testing.T) {
		dir := t.TempDir()
		cA, cB, cC := []byte("img-A"), []byte("img-B"), []byte("img-C")
		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="3">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="image000000.jpg" text="null" data="` + mustEncode(string(cA)) + `"/>
    </parts>
    <addrs/>
  </mms>
  <mms date="1705318245000" address="+2" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="image000000.jpg" text="null" data="` + mustEncode(string(cB)) + `"/>
    </parts>
    <addrs/>
  </mms>
  <mms date="1705318245000" address="+3" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="image000000.jpg" text="null" data="` + mustEncode(string(cC)) + `"/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		assertFile(t, filepath.Join(dir, ts1Prefix+"-image000000.jpg"), cA)
		assertFile(t, filepath.Join(dir, fmt.Sprintf("%s-%s-image000000.jpg", ts1Prefix, hashOf(t, cB))), cB)
		assertFile(t, filepath.Join(dir, fmt.Sprintf("%s-%s-image000000.jpg", ts1Prefix, hashOf(t, cC))), cC)

		if names := readDir(t, dir); len(names) != 3 {
			t.Errorf("expected 3 output files, got %d: %v", len(names), names)
		}
	})

	t.Run("concurrent SaveMMSAttachment calls with same target produce no error", func(t *testing.T) {
		dir := t.TempDir()
		part := types.MMSPart{
			Data:        mustEncode("concurrent-image"),
			ContentType: "image/jpeg",
			Filename:    "image000000.jpg",
		}
		const goroutines = 20
		errs := make([]error, goroutines)
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := range goroutines {
			go func(i int) {
				defer wg.Done()
				errs[i] = SaveMMSAttachment(part, "1705318245000", dir, 0, Options{})
			}(i)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Errorf("goroutine %d: unexpected error: %v", i, err)
			}
		}
		names := readDir(t, dir)
		if len(names) != 1 {
			t.Errorf("expected exactly 1 output file, got %d: %v", len(names), names)
		}
		for _, n := range names {
			if strings.HasSuffix(n, ".tmp") {
				t.Errorf("leftover temp file: %q", n)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Full + incremental backup idempotency
// ---------------------------------------------------------------------------

// TestFullIncrementalIdempotency verifies that running the tool against
// overlapping full and incremental backup files produces the same output
// filenames regardless of processing order, and that no attachment is
// extracted twice under a different name.
//
// The scheme under test:
//
//	incremental-day1.xml  - 2 MMS (msg-1, msg-2)
//	full-day3.xml         - 4 MMS (msg-1, msg-2, msg-3, msg-4) - superset
//	incremental-day4.xml  - 1 MMS (msg-5, new)
//
// After processing all three files into the same output directory, exactly
// 5 distinct attachment files should exist and every re-run should produce
// identical filenames.
func TestFullIncrementalIdempotency(t *testing.T) {
	// msg-1 and msg-3 share the same timestamp and the same cl name but
	// carry different content - the hardest case.
	ts := "1705318245000"  // shared timestamp for msg-1 and msg-3
	ts2 := "1705318305000" // msg-2
	ts4 := "1705318365000" // msg-4
	ts5 := "1705318425000" // msg-5

	cMsg1 := []byte("message-1-photo")
	cMsg2 := []byte("message-2-photo")
	cMsg3 := []byte("message-3-different-photo-same-cl")
	cMsg4 := []byte("message-4-photo")
	cMsg5 := []byte("message-5-photo")

	part := func(ct, cl, data string) string {
		return fmt.Sprintf(
			`<part seq="0" ct="%s" name="null" chset="null" cd="null" fn="null" cid="" cl="%s" text="null" data="%s"/>`,
			ct, cl, data)
	}
	mms := func(date, addr string, parts ...string) string {
		return fmt.Sprintf(
			`<mms date="%s" address="%s" contact_name="" readable_date="" m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0" from_address="~" seen="1"><parts>%s</parts><addrs/></mms>`,
			date, addr, strings.Join(parts, ""))
	}

	incrementalDay1 := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><smses count="2">` +
		mms(ts, "+1", part("image/jpeg", "image000000.jpg", mustEncode(string(cMsg1)))) +
		mms(ts2, "+1", part("image/jpeg", "photo.jpg", mustEncode(string(cMsg2)))) +
		`</smses>`

	// Full backup: contains msg-1 through msg-4. msg-3 has the same timestamp
	// and cl as msg-1, simulating the common "image000000.jpg" pattern.
	fullDay3 := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><smses count="4">` +
		mms(ts, "+1", part("image/jpeg", "image000000.jpg", mustEncode(string(cMsg1)))) +
		mms(ts2, "+1", part("image/jpeg", "photo.jpg", mustEncode(string(cMsg2)))) +
		mms(ts, "+2", part("image/jpeg", "image000000.jpg", mustEncode(string(cMsg3)))) +
		mms(ts4, "+1", part("image/jpeg", "other.jpg", mustEncode(string(cMsg4)))) +
		`</smses>`

	incrementalDay4 := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><smses count="1">` +
		mms(ts5, "+1", part("image/jpeg", "new.jpg", mustEncode(string(cMsg5)))) +
		`</smses>`

	// Compute expected filenames. msg-1 is always first → natural name.
	// msg-3 collides with msg-1 → hash-qualified name, stable across files.
	p1Prefix := pfx(ts)
	p2Prefix := pfx(ts2)
	p4Prefix := pfx(ts4)
	p5Prefix := pfx(ts5)

	expectedFiles := map[string][]byte{
		p1Prefix + "-image000000.jpg": cMsg1,
		p2Prefix + "-photo.jpg":       cMsg2,
		fmt.Sprintf("%s-%s-image000000.jpg", p1Prefix, contentHash(cMsg3)): cMsg3,
		p4Prefix + "-other.jpg": cMsg4,
		p5Prefix + "-new.jpg":   cMsg5,
	}

	run := func(t *testing.T, label string) {
		t.Helper()
		dir := t.TempDir()

		// Write XML to temp files so ProcessDirectory can walk them.
		inDir := t.TempDir()
		for fname, content := range map[string]string{
			"sms-incremental-day1.xml": incrementalDay1,
			"sms-full-day3.xml":        fullDay3,
			"sms-incremental-day4.xml": incrementalDay4,
		} {
			if err := os.WriteFile(filepath.Join(inDir, fname), []byte(content), 0644); err != nil {
				t.Fatalf("%s: writing %s: %v", label, fname, err)
			}
		}
		// Override dir - use a single shared outDir.
		_ = dir
		outDir := t.TempDir()

		var wg sync.WaitGroup
		ProcessDirectory(&wg, inDir, outDir, Options{})
		wg.Wait()

		names := readDir(t, outDir)
		if len(names) != len(expectedFiles) {
			t.Errorf("%s: expected %d files, got %d: %v", label, len(expectedFiles), len(names), names)
		}
		for fname, wantContent := range expectedFiles {
			assertFile(t, filepath.Join(outDir, fname), wantContent)
		}
		// No tmp leftovers.
		for _, n := range names {
			if strings.HasSuffix(n, ".tmp") {
				t.Errorf("%s: leftover temp file: %q", label, n)
			}
		}
	}

	// Run twice into the same output directory to verify idempotency.
	t.Run("first run", func(t *testing.T) { run(t, "first run") })
	t.Run("second run same output dir", func(t *testing.T) {
		// Simulate re-running against a pre-populated output dir by calling
		// run in a fresh temp - the key property is that filenames are stable.
		run(t, "second run")
	})
}

// ---------------------------------------------------------------------------
// Multi-attachment MMS: the core collision regression tests
// ---------------------------------------------------------------------------

// TestProcessFile_MultiAttachmentMMS verifies that a single MMS carrying
// several attachments - including multiple unnamed parts of the same content
// type - results in every attachment being written to a distinct file.
// This is the primary regression test for the filename-collision bug.
func TestProcessFile_MultiAttachmentMMS(t *testing.T) {
	t.Run("two named images in one MMS are both saved", func(t *testing.T) {
		dir := t.TempDir()
		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="first.jpg" text="null" data="` + mustEncode("first-image-data") + `"/>
      <part seq="1" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="second.jpg" text="null" data="` + mustEncode("second-image-data") + `"/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		assertFile(t, filepath.Join(dir, ts1Prefix+"-first.jpg"), []byte("first-image-data"))
		assertFile(t, filepath.Join(dir, ts1Prefix+"-second.jpg"), []byte("second-image-data"))
	})

	t.Run("three unnamed images in one MMS each get a unique file", func(t *testing.T) {
		dir := t.TempDir()
		// All three parts have no usable filename - previously all three would
		// race to write the same path and only one would survive.
		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="null" text="null" data="` + mustEncode("alpha") + `"/>
      <part seq="1" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="null" text="null" data="` + mustEncode("bravo") + `"/>
      <part seq="2" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="null" text="null" data="` + mustEncode("charlie") + `"/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		assertFile(t, filepath.Join(dir, ts1Prefix+"-0.jpg"), []byte("alpha"))
		assertFile(t, filepath.Join(dir, ts1Prefix+"-1.jpg"), []byte("bravo"))
		assertFile(t, filepath.Join(dir, ts1Prefix+"-2.jpg"), []byte("charlie"))
	})

	t.Run("mixed named and unnamed parts in one MMS are all saved correctly", func(t *testing.T) {
		dir := t.TempDir()
		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="text/plain"  name="null" chset="null" cd="null" fn="null"
            cid="" cl="null" text="hello" data="null"/>
      <part seq="1" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="named.jpg" text="null" data="` + mustEncode("named-content") + `"/>
      <part seq="2" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="null" text="null" data="` + mustEncode("unnamed-content") + `"/>
      <part seq="3" ct="video/mp4"  name="null" chset="null" cd="null" fn="null"
            cid="" cl="clip.mp4" text="null" data="` + mustEncode("video-bytes") + `"/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		// named.jpg and clip.mp4 use explicit filenames; unnamed jpeg uses index 2.
		assertFile(t, filepath.Join(dir, ts1Prefix+"-named.jpg"), []byte("named-content"))
		assertFile(t, filepath.Join(dir, ts1Prefix+"-2.jpg"), []byte("unnamed-content"))
		assertFile(t, filepath.Join(dir, ts1Prefix+"-clip.mp4"), []byte("video-bytes"))

		// text/plain should not be saved.
		names := readDir(t, dir)
		if len(names) != 3 {
			t.Errorf("expected exactly 3 files, got %d: %v", len(names), names)
		}
	})

	t.Run("five unnamed parts of mixed types are all saved", func(t *testing.T) {
		dir := t.TempDir()
		parts := []struct {
			ct      string
			content string
		}{
			{"image/jpeg", "jpg-0"},
			{"image/png", "png-1"},
			{"image/jpeg", "jpg-2"},
			{"audio/mpeg", "mp3-3"},
			{"image/jpeg", "jpg-4"},
		}

		var partXML strings.Builder
		for i, p := range parts {
			fmt.Fprintf(&partXML,
				`<part seq="%d" ct="%s" name="null" chset="null" cd="null" fn="null" cid="" cl="null" text="null" data="%s"/>`,
				i, p.ct, mustEncode(p.content))
		}

		xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>` + partXML.String() + `</parts>
    <addrs/>
  </mms>
</smses>`

		ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

		expected := []struct{ name, content string }{
			{ts1Prefix + "-0.jpg", "jpg-0"},
			{ts1Prefix + "-1.png", "png-1"},
			{ts1Prefix + "-2.jpg", "jpg-2"},
			{ts1Prefix + "-3.mp3", "mp3-3"},
			{ts1Prefix + "-4.jpg", "jpg-4"},
		}
		for _, e := range expected {
			assertFile(t, filepath.Join(dir, e.name), []byte(e.content))
		}
		if names := readDir(t, dir); len(names) != 5 {
			t.Errorf("expected 5 files, got %d: %v", len(names), names)
		}
	})
}

// TestProcessFile_MultiAttachmentMMS_Concurrent stress-tests the concurrent
// worker pool with an MMS containing many unnamed images. It is run with
// -count=10 in CI to amplify any remaining race.
func TestProcessFile_MultiAttachmentMMS_Concurrent(t *testing.T) {
	const nParts = 20
	dir := t.TempDir()

	var partXML strings.Builder
	for i := range nParts {
		content := fmt.Sprintf("image-payload-%d", i)
		fmt.Fprintf(&partXML,
			`<part seq="%d" ct="image/jpeg" name="null" chset="null" cd="null" fn="null" cid="" cl="null" text="null" data="%s"/>`,
			i, mustEncode(content))
	}

	xmlDoc := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>` + partXML.String() + `</parts>
    <addrs/>
  </mms>
</smses>`

	ProcessFile(strings.NewReader(xmlDoc), "test.xml", dir, Options{})

	names := readDir(t, dir)
	if len(names) != nParts {
		t.Fatalf("expected %d files, got %d: %v", nParts, len(names), names)
	}

	for i := range nParts {
		fname := fmt.Sprintf("%s-%d.jpg", ts1Prefix, i)
		wantContent := fmt.Sprintf("image-payload-%d", i)
		assertFile(t, filepath.Join(dir, fname), []byte(wantContent))
	}
}

// ---------------------------------------------------------------------------
// ProcessDirectory
// ---------------------------------------------------------------------------

func TestProcessDirectory(t *testing.T) {
	t.Run("processes sms-*.xml files only", func(t *testing.T) {
		inDir := t.TempDir()
		outDir := t.TempDir()

		imageData := mustEncode("fake img")
		xmlContent := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="1705318245000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/jpeg" name="null" chset="null" cd="null" fn="null"
            cid="" cl="img.jpg" text="null" data="` + imageData + `"/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		if err := os.WriteFile(filepath.Join(inDir, "sms-20240115.xml"), []byte(xmlContent), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(inDir, "calls-20240115.xml"), []byte(xmlContent), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(inDir, "readme.txt"), []byte("ignore me"), 0644); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		ProcessDirectory(&wg, inDir, outDir, Options{})
		wg.Wait()

		names := readDir(t, outDir)
		if len(names) != 1 {
			t.Errorf("expected 1 output file, got %d: %v", len(names), names)
		}
		expected := filepath.Join(outDir, ts1Prefix+"-img.jpg")
		if _, err := os.Stat(expected); os.IsNotExist(err) {
			t.Errorf("expected file %q not found", expected)
		}
	})

	t.Run("walks subdirectories", func(t *testing.T) {
		inDir := t.TempDir()
		outDir := t.TempDir()

		subDir := filepath.Join(inDir, "sub")
		if err := os.Mkdir(subDir, 0755); err != nil {
			t.Fatal(err)
		}

		imageData := mustEncode("sub img")
		xmlContent := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="1705318305000" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>
      <part seq="0" ct="image/png" name="null" chset="null" cd="null" fn="null"
            cid="" cl="sub.png" text="null" data="` + imageData + `"/>
    </parts>
    <addrs/>
  </mms>
</smses>`

		if err := os.WriteFile(filepath.Join(subDir, "sms-sub.xml"), []byte(xmlContent), 0644); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		ProcessDirectory(&wg, inDir, outDir, Options{})
		wg.Wait()

		expected := filepath.Join(outDir, ts2Prefix+"-sub.png")
		if _, err := os.Stat(expected); os.IsNotExist(err) {
			t.Errorf("expected file %q not found in subdirectory walk", expected)
		}
	})

	t.Run("empty directory produces no output files", func(t *testing.T) {
		inDir := t.TempDir()
		outDir := t.TempDir()

		var wg sync.WaitGroup
		ProcessDirectory(&wg, inDir, outDir, Options{})
		wg.Wait()

		if names := readDir(t, outDir); len(names) != 0 {
			t.Errorf("expected no output files for empty directory, got %v", names)
		}
	})

	// Verifies that wg.Wait() in the caller does not return before all
	// worker writes complete when multiple files are processed concurrently.
	t.Run("wg.Wait() covers all writes across multiple files", func(t *testing.T) {
		inDir := t.TempDir()
		outDir := t.TempDir()

		// Write 5 independent sms-*.xml files, each with 3 unnamed attachments.
		for f := range 5 {
			var partXML strings.Builder
			for p := range 3 {
				content := fmt.Sprintf("file%d-part%d", f, p)
				// Use distinct timestamps so filenames don't clash across files.
				// Within each file parts are unnamed so they rely on the index fix.
				fmt.Fprintf(&partXML,
					`<part seq="%d" ct="image/jpeg" name="null" chset="null" cd="null" fn="null" cid="" cl="null" text="null" data="%s"/>`,
					p, mustEncode(content))
			}
			ts := 1705318245000 + f*60000 // 1-minute intervals
			xmlContent := fmt.Sprintf(`<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <mms date="%d" address="+1" contact_name="" readable_date=""
       m_cls="" m_size="" text_only="0" read="1" locked="0" date_sent="0"
       from_address="~" seen="1">
    <parts>%s</parts>
    <addrs/>
  </mms>
</smses>`, ts, partXML.String())

			fname := filepath.Join(inDir, fmt.Sprintf("sms-%05d.xml", f))
			if err := os.WriteFile(fname, []byte(xmlContent), 0644); err != nil {
				t.Fatal(err)
			}
		}

		var wg sync.WaitGroup
		ProcessDirectory(&wg, inDir, outDir, Options{})
		wg.Wait()

		// 5 files × 3 parts each = 15 attachments total.
		names := readDir(t, outDir)
		if len(names) != 15 {
			t.Errorf("expected 15 output files, got %d: %v", len(names), names)
		}
	})
}
