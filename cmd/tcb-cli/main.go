// Copyright (c) 2023, nuxencs and the tcb-cli contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gocolly/colly"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

const BaseUrl = "https://tcbscans.com"

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

func getMangas(baseURL string) ([]Manga, error) {
	var mangas []Manga
	var url string
	var name string

	c := colly.NewCollector()

	c.OnHTML("div.bg-card.border.border-border.rounded.p-3.mb-3", func(e *colly.HTMLElement) {
		url = e.ChildAttr("a", "href")
		name = e.ChildAttr("img", "alt")

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

func getChapters(baseURL string, manga Manga) ([]Chapter, error) {
	var chapters []Chapter
	var url string
	var name string
	var title string
	var folder string

	c := colly.NewCollector()

	c.OnHTML("a.block.border.border-border.bg-card.mb-3.p-3.rounded", func(e *colly.HTMLElement) {
		url = e.Attr("href")

		name = strings.TrimSpace(e.ChildText("div.text-lg.font-bold"))
		number, err := getChapterNumber(name)
		if err != nil {
			log.Fatalf("error getting chapter number: %q", err)
		}

		title = getCleanChapterTitle(e.ChildText("div.text-gray-500"))
		folder = filepath.Join(manga.Title, fmt.Sprintf("%g %s", number, title))

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

func downloadImages(p *mpb.Progress, selectedDownloadLocation string, manga Manga, chapter Chapter) error {
	dirPath := filepath.Join(selectedDownloadLocation, manga.Title, fmt.Sprintf("%03g %s", chapter.Number, chapter.Title))
	dirPath = strings.TrimSpace(dirPath)
	err := os.MkdirAll(dirPath, os.ModePerm)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	totalImages := int64(len(chapter.ImageURLs))
	var chapterName = fmt.Sprintf("Chapter %g %s", chapter.Number, chapter.Title)
	bar := p.AddBar(totalImages,
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
				log.Fatalf("error downloading file: %q", err)
			}
			bar.Increment()
		}(i, imageURL)
	}
	wg.Wait()
	p.Wait()
	return nil
}

func getCleanChapterTitle(title string) string {
	// Compile the regex pattern
	r := regexp.MustCompile(`[<>:"/\\|?*]`)

	// Trim spaces
	title = strings.TrimSpace(title)

	// Remove illegal chars
	title = r.ReplaceAllString(title, "")
	return title
}

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
		return number, err
	}
	return 0, err
}

func downloadLocationSelection() (string, error) {
	for {
		fmt.Print("Select a download location\n>> ")
		var selectedDownloadLocation string
		if _, err := fmt.Scan(&selectedDownloadLocation); err != nil {
			fmt.Println("Error reading input. Please try again.")
			continue
		}
		if _, err := os.Stat(selectedDownloadLocation); err == nil {
			return selectedDownloadLocation, nil
		}
		fmt.Println("Invalid selection. Please select a valid location.")
	}
}

func mangaSelection(mangas []Manga) (Manga, error) {
	for i, manga := range mangas {
		fmt.Printf("(%d) %s\n", i+1, manga.Title)
	}

	var selectedManga int
	for {
		fmt.Print("Select a manga\n>> ")
		if _, err := fmt.Scan(&selectedManga); err != nil {
			fmt.Println("Error reading input. Please try again.")
			continue
		}
		if selectedManga >= 1 && selectedManga <= len(mangas) {
			return mangas[selectedManga-1], nil
		}
		fmt.Println("Invalid selection. Please select a valid manga.")
	}
}

func chapterSelection(selectedManga Manga) ([]Chapter, error) {
	var selectedChapters []Chapter
	var availableChapters []float64

	chapters, err := getChapters(BaseUrl, selectedManga)
	if err != nil {
		return nil, err
	}

	sort.SliceStable(chapters, func(i, j int) bool {
		return chapters[i].Number < chapters[j].Number
	})

	for _, chapter := range chapters {
		fmt.Printf("(Chapter %g) %s\n", chapter.Number, chapter.Title)
		availableChapters = append(availableChapters, chapter.Number)
	}

	for {
		fmt.Print("Select chapters\n>> ")
		var input string
		_, err := fmt.Scanln(&input)
		chapterNumbers, err := parseChapterSelection(input, availableChapters)
		if err != nil {
			fmt.Printf("Error parsing selection: %s. Please try again.\n", err.Error())
			continue
		}

		for _, number := range chapterNumbers {
			for _, chapter := range chapters {
				if chapter.Number == number {
					selectedChapters = append(selectedChapters, chapter)
					break
				}
			}
		}
		if len(selectedChapters) > 0 {
			break
		}
		fmt.Println("Invalid selection. Please select valid chapters.")
	}
	return selectedChapters, nil
}

func parseChapterSelection(input string, availableChapters []float64) ([]float64, error) {
	var result []float64
	parts := strings.Split(input, ",")

	for _, part := range parts {
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range format: %s", part)
			}
			start, err := strconv.ParseFloat(strings.TrimSpace(rangeParts[0]), 64)
			if err != nil {
				return nil, err
			}
			end, err := strconv.ParseFloat(strings.TrimSpace(rangeParts[1]), 64)
			if err != nil {
				return nil, err
			}

			for _, availableChapter := range availableChapters {
				if availableChapter >= start && availableChapter <= end {
					result = append(result, availableChapter)
				}
			}
		} else {
			singleChapter, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil {
				return nil, err
			}
			result = append(result, singleChapter)
		}
	}

	// Remove duplicates and sort
	uniqueMap := make(map[float64]bool)
	var uniqueResult []float64
	for _, chapter := range result {
		if !uniqueMap[chapter] {
			uniqueMap[chapter] = true
			uniqueResult = append(uniqueResult, chapter)
		}
	}
	sort.Float64s(uniqueResult)

	return uniqueResult, nil
}

func downloadSelectedChapters(selectedDownloadLocation string, selectedManga Manga, selectedChaptersList []Chapter) {
	var wg sync.WaitGroup
	p := mpb.New()

	for _, selectedChapter := range selectedChaptersList {
		wg.Add(1) // Increment the WaitGroup counter

		go func(chapter Chapter) { // Start a new goroutine for each chapter
			defer wg.Done() // Decrement the counter when the goroutine completes

			selectedChapterImageURLs, err := getImageURLs(BaseUrl, chapter)
			if err != nil {
				log.Fatalf("error getting image urls for Chapter %g: %q", chapter.Number, err)
			}
			chapter.ImageURLs = selectedChapterImageURLs

			err = downloadImages(p, selectedDownloadLocation, selectedManga, chapter)
			if err != nil {
				log.Fatalf("error downloading chapter %g: %q", chapter.Number, err)
			}
		}(selectedChapter)
	}

	wg.Wait() // Wait for all goroutines to finish
}

func main() {
	selectedDownloadLocation, err := downloadLocationSelection()
	if err != nil {
		log.Fatalf("error selecting download location: %q", err)
	}

	mangas, err := getMangas(BaseUrl)
	if err != nil {
		log.Fatalf("error getting mangas: %q", err)
	}

	selectedManga, err := mangaSelection(mangas)
	if err != nil {
		log.Fatalf("error selecting manga: %q", err)
	}

	selectedChaptersList, err := chapterSelection(selectedManga)
	if err != nil {
		log.Fatalf("error selecting chapters: %q", err)
	}

	downloadSelectedChapters(selectedDownloadLocation, selectedManga, selectedChaptersList)
}
