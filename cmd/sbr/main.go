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
	"sync"
	"time"

	"github.com/junkblocker/sbr/types"
)

func processFile(wg *sync.WaitGroup, filePath, outPath string) {
	// fmt.Printf("Processing file: %s\n", filePath)
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
			fmt.Println("Error decoding file:", err)
			break
		}

		switch se := token.(type) {
		case xml.StartElement:
			if se.Name.Local == "calls" {
				fmt.Println("Not an SMS backup:", filePath)
				return
			} else if se.Name.Local == "sms" {
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
					contentType := strings.ToLower(part.ContentType)
					// fmt.Printf("Part Name: %s, Filename: %s\n", part.Name, part.Filename)
					if strings.HasPrefix(contentType, "image/") || strings.HasPrefix(contentType, "audio/") || strings.HasPrefix(contentType, "video/") || contentType == "application/pdf" || contentType == "text/x-vcard" || contentType == "text/v-card" || contentType == "text/vcard" || contentType == "application/octet-stream" {
						wg.Add(1)
						go func(apart types.MMSPart, adate, anOutPath string) {
							defer wg.Done()
							saveMMSAttachment(apart, adate, anOutPath)
						}(part, mms.Date, outPath)
					} else if contentType != "text/plain" && contentType != "application/smil" {
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
	oTempfile := oFile + ".tmp"
	oStat, err := os.Stat(oFile)
	if err == nil {
		if oStat != nil {
			if oStat.IsDir() {
				fmt.Printf("Error: Output path %s is an existing directory\n", oFile)
				// } else {
				// 	fmt.Printf("Output path %s already exists\n", oFile)
			}
			return
		}
	}
	// TODO: Worry about max filename/path length here
	err = os.WriteFile(oTempfile, data, 0644)
	if err != nil {
		fmt.Println("Error saving attachment:", err)
		_ = os.Remove(oTempfile)
		return
	}

	// Set modification time
	err = os.Chtimes(oTempfile, sentTime, sentTime)
	if err != nil {
		fmt.Println("Error setting file time:", err)
		_ = os.Remove(oTempfile)
		return
	}
	if err = os.Rename(oTempfile, oFile); err != nil {
		fmt.Printf("Error renaming temporary file %s to %s: %v\n", oTempfile, oFile, err)
		_ = os.Remove(oTempfile)
		return
	}
	// absPath, _ := filepath.Abs(filename)
	// fmt.Printf("  Attachment saved: %s\n", absPath)
}

func processDirectory(wg *sync.WaitGroup, inDirPath, outDirPath string) {
	// fmt.Printf("Processing directory: %s\n", inDirPath)
	err := filepath.WalkDir(inDirPath, func(apath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fname := entry.Name()
		if !entry.IsDir() {
			if strings.HasPrefix(fname, "sms-") && strings.HasSuffix(fname, ".xml") {
				processFile(wg, apath, outDirPath)
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

	var wg sync.WaitGroup
	if inPathInfo.IsDir() {
		processDirectory(&wg, inPath, outPath)
	} else {
		processFile(&wg, inPath, outPath)
	}
	wg.Wait()
}
