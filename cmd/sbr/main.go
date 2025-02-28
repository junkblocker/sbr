package main

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/junkblocker/sbr/types"
)

func processFile(filePath, outPath string) {
	fmt.Printf("Processing file: %s\n", filePath)
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	decoder := xml.NewDecoder(file)

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}

		switch se := token.(type) {
		case xml.StartElement:
			if se.Name.Local == "sms" {
				var sms types.SMS
				err = decoder.DecodeElement(&sms, &se)
				if err != nil {
					fmt.Println("Error decoding SMS:", err)
					continue
				}
				// fmt.Printf("SMS - Address: %s, Date: %s, Body: %s\n", sms.Address, sms.Date, sms.Body)
			} else if se.Name.Local == "mms" {
				var mms types.MMS
				err = decoder.DecodeElement(&mms, &se)
				if err != nil {
					fmt.Println("Error decoding MMS:", err)
					continue
				}
				// fmt.Printf("MMS - Address: %s, Date: %s\n", mms.Address, mms.Date)
				for _, part := range mms.Parts {
					// fmt.Printf("Part Name: %s, Filename: %s\n", part.Name, part.Filename)
						saveMMSAttachment(part, mms.Date, outPath)
					if strings.HasPrefix(part.ContentType, "image/") || strings.HasPrefix(part.ContentType, "audio/") || strings.HasPrefix(part.ContentType, "video/") || part.ContentType == "application/pdf" || strings.ToLower(part.ContentType) == "text/v-card" || strings.ToLower(part.ContentType) == "text/vcard" || strings.ToLower(part.ContentType) == "application/octet-stream" {
					} else if part.ContentType != "text/plain" && part.ContentType != "application/smil" {
						fmt.Printf("  Unknown: %s\n", part.ContentType)
					}
				}
				// } else if se.Name.Local == "call" {
				// 	var call Call
				// 	err = decoder.DecodeElement(&call, &se)
				// 	if err != nil {
				// 		fmt.Println("Error decoding Call:", err)
				// 		continue
				// 	}
				// 	fmt.Printf("Call: Number=%s, Date=%s, Type=%d\n", call.Number, call.Date, call.Type)
			}
		}
	}
}

func saveMMSAttachment(part types.MMSPart, date, outPath string) {
	data, err := base64.StdEncoding.DecodeString(part.Data)
	if err != nil {
		fmt.Println("Error decoding attachment data:", err)
		return
	}
	// Parse the date
	timestamp, err := strconv.ParseInt(date, 10, 64)
	if err != nil {
		fmt.Println("Error parsing date:", err)
		return
	}
	sentTime := time.Unix(timestamp/1000, 0)

	// Create date prefix
	datePrefix := sentTime.Format("2006-01-02-150405")

	// Determine filename
	filename := ""
	if part.Filename != "" && part.Filename != "null" {
		filename = part.Filename
	} else if part.Name != "" && part.Name != "null" {
		filename = part.Name
	}

	// If filename is still empty, generate a generic name based on content type
	if filename == "" || filename == "null" {
		ext := ".bin"
		switch part.ContentType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/png":
			ext = ".png"
		case "image/gif":
			ext = ".gif"
		case "image/heic":
			ext = ".heic"
		case "video/mp4":
			ext = ".mp4"
		case "video/3gpp":
			ext = ".3gp"
		case "audio/mpeg":
			ext = ".mp3"
		case "audio/amr":
			ext = ".amr"
		case "audio/vnd.qcelp":
			ext = ".qcp"
		case "text/plain":
			ext = ".txt"
		case "application/pdf":
			ext = ".pdf"
		case "text/x-vcard":
			ext = ".vcf"
		case "text/x-vCard":
			ext = ".vcf"
		case "text/xvcard":
			ext = ".vcf"
		case "text/vcard":
			ext = ".vcf"
		}
		filename = datePrefix + ext
	} else {
		filename = datePrefix + "-" + filename
	}

	oFile := filepath.Join(outPath, filename)
	err = os.WriteFile(oFile, data, 0644)
	if err != nil {
		fmt.Println("Error saving attachment:", err)
		return
	}

	// Set modification time
	err = os.Chtimes(oFile, sentTime, sentTime)
	if err != nil {
		fmt.Println("Error setting file time:", err)
		return
	}
	// absPath, _ := filepath.Abs(filename)
	// fmt.Printf("  Attachment saved: %s\n", absPath)
}

func processDirectory(inDirPath, outDirPath string) {
	fmt.Printf("Processing directory: %s\n", inDirPath)
	err := filepath.WalkDir(inDirPath, func(apath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fname := entry.Name()
		if !entry.IsDir() {
			if strings.HasPrefix(fname, "sms-") && strings.HasSuffix(fname, ".xml") {
				processFile(apath, outDirPath)
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("Failed to walk directory %s: %v\n", inDirPath, err)
	}
}

func main() {
	if len(os.Args) != 3 {
		fmt.Println("Usage: go run main.go <file_or_directory_path> <output_dir>")
		os.Exit(1)
	}

	inPath := os.Args[1]
	outPath := os.Args[2]
	inPathInfo, err := os.Stat(inPath)
	if err != nil {
		log.Fatalf("Error accessing path %s: %v\n", inPath, err)
	}
	outPathInfo, err := os.Stat(outPath)
	if err != nil {
		if err = os.Mkdir(outPath, 0755); err != nil {
			log.Fatalf("Error creating directory %s: %v\n", outPath, err)
		}
	} else if outPathInfo != nil && !outPathInfo.IsDir() {
		log.Fatalf("Output path %s is not a directory\n", outPath)
	}

	if inPathInfo.IsDir() {
		processDirectory(inPath, outPath)
	} else {
		processFile(inPath, outPath)
	}
}
