// Copyright (c) 2023, nuxencs and the tcb-cli contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/fatih/color"
	"github.com/gocolly/colly"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

const BaseUrl = "https://tcbscans.com"

var (
	blue       = color.New(color.FgBlue).Add(color.Bold)
	green      = color.New(color.FgHiGreen)
	greenBold  = color.New(color.FgHiGreen).Add(color.Bold)
	red        = color.New(color.FgRed)
	yellow     = color.New(color.FgHiYellow)
	yellowBold = color.New(color.FgHiYellow).Add(color.Bold)
)

type Manga struct {
	URL   string
	Title string
}

type Chapter struct {
	URL       string
	Number    float64
	Title     string
	ImageURLs []string
	Folder    string
}

// getMangas gets all mangas
func getMangas(baseURL string) ([]Manga, error) {
	var mangas []Manga

	c := colly.NewCollector()

	c.OnHTML("div.bg-card.border.border-border.rounded.p-3.mb-3", func(e *colly.HTMLElement) {
		url := e.ChildAttr("a", "href")
		name := e.ChildAttr("img", "alt")

		mangas = append(mangas, Manga{
			URL:   url,
			Title: name},
		)
	})

	err := c.Visit(baseURL + "/projects")
	if err != nil {
		return []Manga{}, err
	}

	return mangas, nil
}

// getChapters gets all chapters for a manga
func getChapters(baseURL string, manga Manga) ([]Chapter, error) {
	var chapters []Chapter

	c := colly.NewCollector()

	c.OnHTML("a.block.border.border-border.bg-card.mb-3.p-3.rounded", func(e *colly.HTMLElement) {
		url := e.Attr("href")

		name := strings.TrimSpace(e.ChildText("div.text-lg.font-bold"))
		number, err := getChapterNumber(name)
		if err != nil {
			red.Printf("error getting chapter number: %q", err)
			os.Exit(1)
		}

		title := getCleanChapterTitle(e.ChildText("div.text-gray-500"))
		folder := filepath.Join(manga.Title, fmt.Sprintf("%g %s", number, title))

		chapters = append(chapters, Chapter{
			URL:    url,
			Number: number,
			Title:  title,
			Folder: folder,
		})
	})

	err := c.Visit(baseURL + manga.URL)
	if err != nil {
		return []Chapter{}, err
	}

	return chapters, nil
}

// getImageURLs gets all image urls for a chapter
func getImageURLs(baseURL string, chapter Chapter) ([]string, error) {
	var imageURLs []string

	c := colly.NewCollector()

	c.OnHTML("img.fixed-ratio-content", func(e *colly.HTMLElement) {
		imageURLs = append(imageURLs, e.Attr("src"))
	})

	err := c.Visit(baseURL + chapter.URL)
	if err != nil {
		return nil, err
	}

	return imageURLs, nil
}

// downloadImage downloads a single image
func downloadImage(url, filename string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}
	return nil
}

// downloadImages downloads all images from a selected chapter
func downloadImages(p *mpb.Progress, selectedDownloadLocation string, manga Manga, chapter Chapter, createCbz bool) error {
	var wg sync.WaitGroup

	dirPath := filepath.Join(selectedDownloadLocation, manga.Title, fmt.Sprintf("%03g %s", chapter.Number, chapter.Title))
	dirPath = strings.TrimSpace(dirPath)
	err := os.MkdirAll(dirPath, os.ModePerm)
	if err != nil {
		return err
	}

	var chapterName = greenBold.Sprintf("(%g) ", chapter.Number) + green.Sprintf("%s", chapter.Title)
	bar := p.AddBar(int64(len(chapter.ImageURLs)),
		mpb.PrependDecorators(
			decor.Name(chapterName),
			decor.CountersNoUnit(" %d / %d"),
		),
		mpb.AppendDecorators(
			decor.Percentage(),
		),
	)

	for i, imageURL := range chapter.ImageURLs {
		wg.Add(1)

		go func(i int, imageURL string) {
			defer wg.Done()
			extension := filepath.Ext(imageURL)
			filename := filepath.Join(dirPath, fmt.Sprintf("%03d%s", i+1, extension))
			err = downloadImage(imageURL, filename)
			if err != nil {
				red.Printf("error downloading file: %q", err)
				os.Exit(1)
			}
			bar.Increment()
		}(i, imageURL)
	}
	wg.Wait()

	if createCbz {
		cbzFilename := filepath.Join(selectedDownloadLocation, manga.Title, fmt.Sprintf("%03g %s.cbz", chapter.Number, chapter.Title))
		err = createCbzArchive(dirPath, cbzFilename)
		if err != nil {
			return err
		}

		// delete the image directory after creating the CBZ
		err = os.RemoveAll(dirPath)
		if err != nil {
			return err
		}
	}

	return nil
}

// addFileToZip creates a zip archive named cbzFilename and adds all files from sourceDir to it
func createCbzArchive(sourceDir, cbzFilename string) error {
	// Create a new zip archive
	cbzFile, err := os.Create(cbzFilename)
	if err != nil {
		return err
	}
	defer cbzFile.Close()

	zipWriter := zip.NewWriter(cbzFile)
	defer func() {
		if err := zipWriter.Close(); err != nil {
			fmt.Println("Error closing zip writer:", err)
		}
	}()

	// Walk through the directory and add files to the zip
	err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return addFileToZip(zipWriter, path, info.Name())
		}
		return nil
	})

	return err
}

// addFileToZip adds a single file to the zip archive
func addFileToZip(zipWriter *zip.Writer, filePath, fileName string) error {
	fileToZip, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fileToZip.Close()

	// Create a writer for this file in the zip
	writer, err := zipWriter.Create(fileName)
	if err != nil {
		return err
	}

	// Copy the file data to the zip
	_, err = io.Copy(writer, fileToZip)
	return err
}

// getCleanChapterTitle removes problematic characters from the chapter title
func getCleanChapterTitle(title string) string {
	// Compile the regex pattern
	r := regexp.MustCompile(`[<>:"/\\|?*]`)

	// Trim spaces & dots
	title = strings.Trim(title, " .")

	// Remove illegal chars
	title = r.ReplaceAllString(title, "")
	return title
}

// getChapterNumber gets the chapter number from the scraped chapter name
func getChapterNumber(name string) (float64, error) {
	var number float64

	// Compile the regex pattern
	r, err := regexp.Compile(`Chapter (\d+(\.\d+)?)`)
	if err != nil {
		return 0, err
	}

	// FindSubmatch returns an array where the first element is the full match, and the rest are submatches.
	matches := r.FindStringSubmatch(name)
	if len(matches) > 1 {
		number, err = strconv.ParseFloat(matches[1], 64)
		if err != nil {
			return 0, err
		}
		return number, nil
	}
	return 0, err
}

// downloadLocationSelection asks the user for a download location
func downloadLocationSelection() (string, error) {
	for {
		blue.Println("Select a download location")
		fmt.Print(">> ")
		var selectedDownloadLocation string
		if _, err := fmt.Scan(&selectedDownloadLocation); err != nil {
			red.Println("Error reading input. Please try again.")
			continue
		}
		if _, err := os.Stat(selectedDownloadLocation); err == nil {
			return selectedDownloadLocation, nil
		}
		red.Println("Invalid selection. Please select a valid location.")
	}
}

// promptForCbzCreation asks the user if they want to create a CBZ archive and handles invalid input
func promptForCbzCreation() bool {
	reader := bufio.NewReader(os.Stdin)

	for {
		blue.Println("Would you like a cbz archive to be created? (y/N)")
		fmt.Print(">> ")
		response, err := reader.ReadString('\n')
		if err != nil {
			red.Println("Error reading input. Please try again.")
			continue
		}

		response = strings.ToLower(strings.TrimSpace(response))

		switch response {
		case "y", "yes":
			return true
		case "n", "no", "":
			return false
		default:
			red.Println("Invalid input. Please enter 'y' for yes or 'n' for no.")
		}
	}
}

// mangaSelection asks the user to select a manga
func mangaSelection(mangas []Manga) (Manga, error) {
	mangaMap := make(map[int]Manga)
	for i, manga := range mangas {
		mangaMap[i+1] = manga
		yellowBold.Printf("(%d) ", i+1)
		yellow.Printf("%s\n", manga.Title)
	}

	var selectedManga int
	for {
		blue.Println("Select a manga")
		fmt.Print(">> ")
		if _, err := fmt.Scan(&selectedManga); err != nil {
			red.Println("Error reading input. Please try again.")
			continue
		}
		if manga, ok := mangaMap[selectedManga]; ok {
			return manga, nil
		}
		red.Println("Invalid selection. Please select a valid manga.")
	}
}

// chapterSelection asks the user to select the chapters to download
func chapterSelection(selectedManga Manga) ([]Chapter, error) {
	allChapters, err := getChapters(BaseUrl, selectedManga)
	if err != nil {
		return nil, err
	}

	sort.SliceStable(allChapters, func(i, j int) bool {
		return allChapters[i].Number < allChapters[j].Number
	})

	// Create a map for easy access to chapters by number
	chapterMap := make(map[float64]Chapter)
	for _, chapter := range allChapters {
		chapterMap[chapter.Number] = chapter
		yellowBold.Printf("(%g) ", chapter.Number)
		yellow.Printf("%s\n", chapter.Title)
	}

	chapterNumbers, err := getUserChapterSelection(allChapters)
	if err != nil {
		return nil, err
	}

	return getSelectedChapters(chapterNumbers, chapterMap), nil
}

// getUserChapterSelection asks the user to select a manga
func getUserChapterSelection(chapters []Chapter) ([]float64, error) {
	blue.Println("Select chapters")
	fmt.Print(">> ")
	var input string
	if _, err := fmt.Scan(&input); err != nil {
		return nil, fmt.Errorf("error reading input: %q", err)
	}
	return parseChapterSelection(input, getChapterNumbers(chapters))
}

// getChapterNumbers gets all chapter numbers from a provided chapter slice
func getChapterNumbers(chapters []Chapter) []float64 {
	var numbers []float64
	for _, chapter := range chapters {
		numbers = append(numbers, chapter.Number)
	}
	return numbers
}

// getSelectedChapters gets selected chapters from the user selected chapter numbers
func getSelectedChapters(selectedNumbers []float64, chapterMap map[float64]Chapter) []Chapter {
	var selectedChapters []Chapter
	for _, num := range selectedNumbers {
		if chapter, ok := chapterMap[num]; ok {
			selectedChapters = append(selectedChapters, chapter)
		}
	}
	return selectedChapters
}

// parseChapterSelection parses the user input for ranges and parts
func parseChapterSelection(input string, availableChapters []float64) ([]float64, error) {
	parts := strings.Split(input, ",")
	chapterMap := make(map[float64]bool)

	for _, part := range parts {
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range format: %s", part)
			}
			start, end, err := parseRange(rangeParts)
			if err != nil {
				return nil, err
			}

			for _, chapter := range availableChapters {
				if chapter >= start && chapter <= end {
					chapterMap[chapter] = true
				}
			}
		} else {
			chapter, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil {
				return nil, err
			}
			chapterMap[chapter] = true
		}
	}

	return mapToSlice(chapterMap), nil
}

// parseRange parses the user input for chapter ranges
func parseRange(rangeParts []string) (float64, float64, error) {
	start, err := strconv.ParseFloat(strings.TrimSpace(rangeParts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid start of range: %s", rangeParts[0])
	}
	end, err := strconv.ParseFloat(strings.TrimSpace(rangeParts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid end of range: %s", rangeParts[1])
	}

	if start > end {
		return 0, 0, fmt.Errorf("start of range should not be greater than end: %s-%s", rangeParts[0], rangeParts[1])
	}

	return start, end, nil
}

// mapToSlice converts a map to a sorted slice
func mapToSlice(chapterMap map[float64]bool) []float64 {
	var result []float64
	for chapter := range chapterMap {
		result = append(result, chapter)
	}
	sort.Float64s(result)
	return result
}

// downloadSelectedChapters downloads user selected chapters
func downloadSelectedChapters(selectedDownloadLocation string, selectedManga Manga, selectedChaptersList []Chapter, createCbz bool) {
	var wg sync.WaitGroup
	p := mpb.New(mpb.WithWaitGroup(&wg))

	for _, selectedChapter := range selectedChaptersList {
		wg.Add(1)
		go func(chapter Chapter) { // Start a new goroutine for each chapter
			defer wg.Done() // Decrement the counter when the goroutine completes

			selectedChapterImageURLs, err := getImageURLs(BaseUrl, chapter)
			if err != nil {
				red.Printf("error getting image urls for Chapter %g: %q", chapter.Number, err)
				os.Exit(1)
			}
			chapter.ImageURLs = selectedChapterImageURLs

			err = downloadImages(p, selectedDownloadLocation, selectedManga, chapter, createCbz)
			if err != nil {
				red.Printf("error downloading chapter %g: %q", chapter.Number, err)
				os.Exit(1)
			}
		}(selectedChapter)
	}

	p.Wait() // Wait for all goroutines to finish
}

func main() {
	selectedDownloadLocation, err := downloadLocationSelection()
	if err != nil {
		red.Printf("error selecting download location: %q", err)
		os.Exit(1)
	}

	createCbz := promptForCbzCreation()

	mangas, err := getMangas(BaseUrl)
	if err != nil {
		red.Printf("error getting mangas: %q", err)
		os.Exit(1)
	}

	selectedManga, err := mangaSelection(mangas)
	if err != nil {
		red.Printf("error selecting manga: %q", err)
		os.Exit(1)
	}

	selectedChaptersList, err := chapterSelection(selectedManga)
	if err != nil {
		red.Printf("error selecting chapters: %q", err)
		os.Exit(1)
	}

	downloadSelectedChapters(selectedDownloadLocation, selectedManga, selectedChaptersList, createCbz)
}
