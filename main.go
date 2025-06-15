package main

import (
	"archive/zip"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/muesli/termenv"
)

var version string = "dev"

var (
	debug       bool
	storagePath = "/data"
	tmpPath     = "/tmp"
)

var line = strings.Repeat("-", 80)

var selfHostSuffixes = []string{
	"basket:9002",
	"basket:9004",
	"localhost:9004",
	"localhost:9002",
}

//go:embed assets/zip.html
var zipTemplate string

func init() {
	log.SetColorProfile(termenv.ANSI)
	rawDebug := os.Getenv("DEBUG")
	if rawDebug != "" {
		if rawDebug == "true" || rawDebug == "1" {
			log.SetLevel(log.DebugLevel)
		}
	}
}

type (
	ZipFile struct {
		Filepath   string
		Filename   string
		Base64Path string
	}
	ZipContent struct {
		Host     string
		Filename string
		ZipPath  string
		Files    []ZipFile
	}
)

func main() {

	for _, path := range []string{storagePath, tmpPath} {
		// create the storage directory if it doesn't exist
		err := os.MkdirAll(path, 0755)
		if err != nil {
			log.Fatal("Error creating storage directory", "error", err)
		}
	}

	rootHandler := http.FileServer(http.Dir(storagePath))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		bucket := r.Host

		for _, suffix := range selfHostSuffixes {
			bucket = strings.Replace(bucket, "."+suffix, "", -1)
		}
		for _, suffix := range selfHostSuffixes {
			bucket = strings.Replace(bucket, suffix, "", -1)
		}

		filename := r.URL.Path
		filename = strings.TrimPrefix(filename, "/"+bucket)

		filename = filepath.Join(storagePath, bucket, filename)
		filename = filepath.Clean(filename)

		log.Debug("Request",
			"host", r.Host,
			"bucket", bucket,
			"path", r.URL.Path,
			"filename", filename,
		)

		// dump the request
		data, err := httputil.DumpRequest(r, false)
		if err != nil {
			log.Warn("Error dumping request", "error", err)
		} else {
			log.Debugf("%s%s%s",
				fmt.Sprintln(line),
				string(data),
				fmt.Sprintln(line),
			)
		}

		switch r.Method {
		case http.MethodGet:

			// Check if it's a ZIP file
			if strings.HasSuffix(filename, ".zip") {

				download := r.URL.Query().Get("d") == "true"
				nestedPath := r.URL.Query().Get("p")

				if nestedPath == "" && !download {

					log.Debug("Serving ZIP file",
						"filename", filename,
					)

					// if the d query param is provided with true then download the file

					if err := serveZipContents(w, r.Host, filename); err != nil {
						http.Error(w, "Error reading ZIP file: "+err.Error(), http.StatusInternalServerError)
					}

					return

				} else if nestedPath != "" {

					raw, err := base64.URLEncoding.DecodeString(nestedPath)
					if err != nil {
						http.Error(w, "Error decoding base64 path: "+err.Error(), http.StatusInternalServerError)
						return
					}

					nestedPath = string(raw)

					log.Debug("Serving nested file from ZIP",
						"filename", filename,
						"nestedPath", nestedPath,
					)

					tmpDir, err := unzipFileToTmpDir(filename)
					if err != nil {
						http.Error(w, "Error unzipping file: "+err.Error(), http.StatusInternalServerError)
					}

					file, err := os.Open(filepath.Join(tmpDir, nestedPath))
					if err != nil {
						http.Error(w, "Error opening nested file: "+err.Error(), http.StatusInternalServerError)
						return
					}

					info, err := file.Stat()
					if err != nil {
						http.Error(w, "Error getting file info: "+err.Error(), http.StatusInternalServerError)
						return
					}

					raw, err = io.ReadAll(file)
					if err != nil {
						http.Error(w, "Error reading file: "+err.Error(), http.StatusInternalServerError)
						return
					}

					// Set the headers
					w.Header().Set("Content-Type", http.DetectContentType(raw))
					w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(nestedPath)))

					http.ServeContent(w, r, nestedPath, info.ModTime(), file)

					return

				}
			}

			log.Debug("Serving regular file",
				"filename", filename,
			)
			rootHandler.ServeHTTP(w, r)
			return

		case http.MethodPut, http.MethodPost:

			err := os.MkdirAll(filepath.Dir(filename), 0755)
			if err != nil {
				http.Error(w, "Error creating directory", http.StatusInternalServerError)
				return
			}

			file, err := os.Create(filename)
			if err != nil {
				http.Error(w, "Error creating file", http.StatusInternalServerError)
				return
			}

			_, err = io.Copy(file, r.Body)
			if err != nil {
				http.Error(w, "Error copying file", http.StatusInternalServerError)
				return
			}

			err = file.Close()
			if err != nil {
				http.Error(w, "Error closing file", http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusCreated)
			return

		case http.MethodDelete:
			_, err := os.Stat(filename)
			if err != nil {
				if os.IsNotExist(err) {
					http.Error(w, "File not found", http.StatusNotFound)
					return
				}
				http.Error(w, "Error checking file", http.StatusInternalServerError)
				return
			}

			err = os.Remove(filename)
			if err != nil {
				http.Error(w, "Error deleting file", http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusNoContent)
			return

		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return

		}
	})

	log.Info("Starting server on :9002")
	http.ListenAndServe(":9002", nil)
}

func unzipFileToTmpDir(zipPath string) (string, error) {

	// Open the ZIP file
	zipFile, err := os.Open(zipPath)
	if err != nil {
		return "", fmt.Errorf("Error opening ZIP file: %w", err)
	}
	defer zipFile.Close()

	// Read ZIP file contents
	zipReader, err := zip.NewReader(zipFile, fileSize(zipPath))
	if err != nil {
		return "", fmt.Errorf("Error reading ZIP file: %w", err)
	}

	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "basket")
	if err != nil {
		return "", fmt.Errorf("Error creating temporary directory: %w", err)
	}

	// Extract ZIP file contents
	for _, f := range zipReader.File {
		name := f.FileInfo().Name()
		path := filepath.Join(tmpDir, name)

		if f.FileInfo().IsDir() {
			err := os.MkdirAll(path, 0755)
			if err != nil {
				return "", fmt.Errorf("Error creating directory: %w", err)
			}
			continue
		}

		// Create parent directories
		err := os.MkdirAll(filepath.Dir(path), 0755)
		if err != nil {
			return "", fmt.Errorf("Error creating parent directories: %w", err)
		}

		// Open file in ZIP
		zipFile, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("Error opening file in ZIP: %w", err)
		}
		defer zipFile.Close()

		// Create file on disk
		file, err := os.Create(path)
		if err != nil {
			return "", fmt.Errorf("Error creating file: %w", err)
		}
		defer file.Close()

		log.Debug("Unzipping file",
			"filename", path,
		)

		// Copy contents
		_, err = io.Copy(file, zipFile)
		if err != nil {
			return "", fmt.Errorf("Error copying file contents: %w", err)
		}
	}

	return tmpDir, nil
}

func serveZipContents(w http.ResponseWriter, host, filePath string) error {
	// Open the ZIP file
	zipFile, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("Error opening ZIP file: %w", err)
	}
	defer zipFile.Close()

	zipPath := strings.TrimPrefix(filePath, storagePath)

	size := fileSize(filePath)

	log.Debug("Reading ZIP file",
		"filename", filePath,
		"size", size,
	)

	// Read ZIP file contents
	zipReader, err := zip.NewReader(zipFile, size)
	if err != nil {
		return fmt.Errorf("Error reading ZIP file: %w", err)
	}

	// Extract file names
	var files []ZipFile
	for _, f := range zipReader.File {

		name := f.FileInfo().Name()

		filenameWithoutPath := filepath.Base(name)
		filepathWithoutName := filepath.Dir(name)
		fullPath := filepath.Join(filepath.Dir(filePath), filepathWithoutName)
		base64EncodedPath := base64.URLEncoding.EncodeToString([]byte(name))

		files = append(files, ZipFile{
			Filepath:   fullPath,
			Filename:   filenameWithoutPath,
			Base64Path: base64EncodedPath,
		})
	}

	// Render HTML template with file names
	tmpl, err := template.New("zip").Parse(zipTemplate)
	if err != nil {
		return fmt.Errorf("Error parsing ZIP template: %w", err)
	}

	log.Debug("Serving ZIP file contents",
		"host", host,
		"zipPath", zipPath,
		"filename", filepath.Base(filePath),
	)

	return tmpl.Execute(w, ZipContent{
		Host:     host,
		ZipPath:  zipPath,
		Filename: filepath.Base(filePath),
		Files:    files,
	})
}

func fileSize(filePath string) int64 {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0
	}
	return info.Size()
}
