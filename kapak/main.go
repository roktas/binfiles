package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-pdf/fpdf"
)

const (
	defaultGridSize   = "3x6"
	defaultOutputName = "output.pdf"
	drPrimaryURLFmt   = "https://i.dr.com.tr/cache/500x400-0/originals/%s-1.jpg"
	drBackupURLFmt    = "https://i.dr.com.tr/cache/500x400-0/originals/%s.jpg"
	httpUserAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"
	pageMarginXMM     = 20.0
	pageMarginYMM     = 20.0
	cellBorderInsetMM = 2.0
	contentPaddingMM  = 10.0
	cellBorderWidth   = 0.3
	cellBorderGray    = 160
	httpTimeout       = 15 * time.Second
)

// Converts Turkish characters to ASCII for PDF safety
func toASCII(s string) string {
	replacer := strings.NewReplacer(
		"ğ", "g", "Ğ", "G",
		"ü", "u", "Ü", "U",
		"ş", "s", "Ş", "S",
		"ı", "i", "İ", "I",
		"ö", "o", "Ö", "O",
		"ç", "c", "Ç", "C",
	)
	return replacer.Replace(s)
}

// Draws ASCII-safe text inside a PDF cell
func drawAsciiText(pdf *fpdf.Fpdf, x, y, w, h float64, text string) {
	pdf.SetFont("Arial", "B", 8)
	pdf.SetXY(x, y+(h/2)-2)
	safeText := toASCII(text)
	pdf.CellFormat(w, 5, safeText, "", 0, "C", false, 0, "")
}

func parseGridSize(value string) (int, int, error) {
	clean := strings.ToLower(strings.TrimSpace(value))
	parts := strings.Split(clean, "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("grid size must be rowxcol")
	}

	rows, err := strconv.Atoi(parts[0])
	if err != nil || rows <= 0 {
		return 0, 0, fmt.Errorf("row value must be positive")
	}

	cols, err := strconv.Atoi(parts[1])
	if err != nil || cols <= 0 {
		return 0, 0, fmt.Errorf("column value must be positive")
	}

	return rows, cols, nil
}

func scanIDs(r io.Reader) ([]string, error) {
	var validIDs []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		extractedID := extractProductCode(line)
		if extractedID != "" {
			validIDs = append(validIDs, extractedID)
		}
	}
	return validIDs, scanner.Err()
}

func extractProductCode(line string) string {
	if isAllDigits(line) {
		return line
	}
	target := "urunno="
	if idx := strings.Index(line, target); idx != -1 {
		rest := line[idx+len(target):]
		var sb strings.Builder
		for _, r := range rest {
			if unicode.IsDigit(r) {
				sb.WriteRune(r)
			} else {
				break
			}
		}
		if sb.Len() > 0 {
			return sb.String()
		}
	}
	return ""
}

func isAllDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func fetchDRImage(client *http.Client, id string) ([]byte, string, error) {
	url := fmt.Sprintf(drPrimaryURLFmt, id)
	data, err := download(client, url)
	if err == nil {
		return data, detectFormat(data), nil
	}

	urlBackup := fmt.Sprintf(drBackupURLFmt, id)
	data, err = download(client, urlBackup)
	if err == nil {
		return data, detectFormat(data), nil
	}

	return nil, "", fmt.Errorf("image not found")
}

func download(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status: %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func detectFormat(data []byte) string {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "JPG"
	}
	if format == "jpeg" {
		return "JPG"
	}
	if format == "png" {
		return "PNG"
	}
	return strings.ToUpper(format)
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [input_file]\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Downloads D&R cover images and renders them on an A4 PDF grid.")
		fmt.Fprintln(os.Stderr, "\nDetails:")
		fmt.Fprintln(os.Stderr, "  - Output: Input filename is reused with .pdf extension.")
		fmt.Fprintln(os.Stderr, "  - Stdin: When no file argument is provided, reads stdin and writes output.pdf.")
		fmt.Fprintln(os.Stderr, "  - Text: All strings are converted to ASCII for PDF rendering.")
		fmt.Fprintln(os.Stderr, "  - Comments: Lines starting with '#' are ignored.")
		fmt.Fprintln(os.Stderr, "\nExamples:")
		fmt.Fprintln(os.Stderr, "  go run . books.txt      -> books.pdf")
		fmt.Fprintln(os.Stderr, "  cat links.txt | go run . -> output.pdf")
		flag.PrintDefaults()
	}

	sizeFlag := flag.String("size", defaultGridSize, "Grid size as rowxcol (e.g., 3x6)")
	flag.Parse()

	rows, cols, err := parseGridSize(*sizeFlag)
	if err != nil {
		fmt.Printf("Invalid grid size: %v\n", err)
		os.Exit(1)
	}

	var reader io.Reader
	var sourceName string
	var outputName string

	if flag.NArg() > 0 {
		filename := flag.Arg(0)
		f, err := os.Open(filename)
		if err != nil {
			fmt.Printf("Unable to open file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		reader = f
		sourceName = filename

		ext := filepath.Ext(filename)
		outputName = filename[0:len(filename)-len(ext)] + ".pdf"
	} else {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) != 0 {
			fmt.Println("Awaiting stdin input... (CTRL+D to finish)")
		}
		reader = os.Stdin
		sourceName = "stdin"
		outputName = defaultOutputName
	}

	ids, err := scanIDs(reader)
	if err != nil {
		fmt.Printf("Read error: %v\n", err)
		return
	}

	if len(ids) == 0 {
		fmt.Println("No valid product code detected.")
		return
	}

	fmt.Printf("Source: %s | Target: %s | %d codes will be processed.\n", sourceName, outputName, len(ids))

	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetFont("Arial", "", 12)
	pdf.AddPage()

	width, height := pdf.GetPageSize()

	cellsPerPage := rows * cols
	cellWidth := (width - (2 * pageMarginXMM)) / float64(cols)
	cellHeight := (height - (2 * pageMarginYMM)) / float64(rows)

	client := &http.Client{Timeout: httpTimeout}

	for i, id := range ids {
		if i > 0 && i%cellsPerPage == 0 {
			pdf.AddPage()
		}

		pageIndex := i % cellsPerPage
		row := pageIndex / cols
		col := pageIndex % cols

		x := pageMarginXMM + (float64(col) * cellWidth)
		y := pageMarginYMM + (float64(row) * cellHeight)

		fmt.Printf("[%02d/%02d] Downloading ID: %s\n", i+1, len(ids), id)

		pdf.SetLineWidth(cellBorderWidth)
		pdf.SetDrawColor(cellBorderGray, cellBorderGray, cellBorderGray)
		pdf.Rect(x+cellBorderInsetMM, y+cellBorderInsetMM, cellWidth-(2*cellBorderInsetMM), cellHeight-(2*cellBorderInsetMM), "D")
		pdf.SetDrawColor(0, 0, 0)

		imgData, format, err := fetchDRImage(client, id)

		if err == nil && imgData != nil {
			imgConfig, _, errDecode := image.DecodeConfig(bytes.NewReader(imgData))
			if errDecode != nil {
				drawAsciiText(pdf, x, y, cellWidth, cellHeight, "INVALID FORMAT")
				continue
			}

			aspect := float64(imgConfig.Height) / float64(imgConfig.Width)
			displayW := cellWidth - contentPaddingMM
			displayH := displayW * aspect

			if displayH > (cellHeight - contentPaddingMM) {
				displayH = cellHeight - contentPaddingMM
				displayW = displayH / aspect
			}

			centerX := x + (cellWidth-displayW)/2
			centerY := y + (cellHeight-displayH)/2

			imageName := fmt.Sprintf("img_%d", i)
			opt := fpdf.ImageOptions{ImageType: format, ReadDpi: true}

			pdf.RegisterImageOptionsReader(imageName, opt, bytes.NewReader(imgData))
			pdf.ImageOptions(imageName, centerX, centerY, displayW, displayH, false, opt, 0, "")

		} else {
			drawAsciiText(pdf, x, y, cellWidth, cellHeight, "NOT FOUND")

			pdf.SetFont("Arial", "", 8)
			pdf.SetXY(x, y+cellHeight-contentPaddingMM)
			safeID := toASCII(id)
			pdf.CellFormat(cellWidth, 5, safeID, "", 0, "C", false, 0, "")
		}
	}

	if err := pdf.OutputFileAndClose(outputName); err != nil {
		fmt.Println("Failed to save PDF:", err)
	} else {
		fmt.Printf("Success! File saved: %s\n", outputName)
	}
}
