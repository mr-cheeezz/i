package main

import (
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// the address to listen on
	address = "127.0.0.1:9005"
	// the public URL used when returning uploaded file links
	publicBaseURL = "https://i.mrcheeezz.com"
	// the directory to save the images in
	root = "/mnt/storage/uploads/i/"

	// maximum age for the files
	// the program will delete the files older than maxAge every 2 hours
	// default: everything else = 7 days
	maxAge = time.Hour * 24 * 7
	// per-category retention sourced from filetypes.json category names
	categoryMaxAge = map[string]time.Duration{
		"image":    time.Hour * 24,
		"icon":     time.Hour * 24,
		"code":     time.Hour * 24 * 30,
		"script":   time.Hour * 24 * 30,
		"document": time.Hour * 24 * 30,
	}
	// extension -> category loaded from filetypes.json
	filetypes = make(map[string]string)
	// files to be ignored when deleting old files
	deleteIgnoreRegexp = regexp.MustCompile(`index\\.html|favicon\\.ico`)

	// length of the random filename
	randomFilenameLength = 6

	// permanent uploads are stored in this directory under root
	// set form/query field `permanent=1` to use it for an upload
	// set defaultPermanentUploads to true to make permanent storage the default behavior
	permanentSubdir         = "saved"
	permanentFormFlag       = "permanent"
	defaultPermanentUploads = false
)

func main() {
	if err := loadFiletypes("./filetypes.json"); err != nil {
		fmt.Printf("warning: failed to load filetypes.json: %v\n", err)
	}

	if err := os.MkdirAll(filepath.Join(root, permanentSubdir), 0755); err != nil {
		panic(err)
	}

	go func() {
		for {
			<-time.After(time.Hour * 2)
			collectGarbage()
		}
	}()

	// create server with read and write timeouts and the desired address
	server := &http.Server{
		ReadTimeout:  time.Minute,
		WriteTimeout: time.Minute,
		Addr:         address,
	}

	// open http server
	http.HandleFunc("/", handleUpload)
	server.ListenAndServe()
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	infile, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error parsing uploaded file: "+err.Error(), http.StatusBadRequest)
		return
	}

	defer infile.Close()

	filename := header.Filename
	var ext string

	// get extension from file name
	index := strings.LastIndex(filename, ".")

	if index == -1 {
		ext = ""
	} else {
		ext = filename[index:]
		filename = filename[:index]
	}

	var savePath string
	var random string
	isPermanent := shouldStorePermanently(r)
	targetDir := root

	if isPermanent {
		targetDir = filepath.Join(root, permanentSubdir)
	}

	// find a random filename that doesn't exist already
	available := false
	for i := 0; i < 100; i++ {
		random, err = generateRandomName(randomFilenameLength)
		if err != nil {
			http.Error(w, "error while generating file name: "+err.Error(), http.StatusInternalServerError)
			return
		}

		savePath = filepath.Join(targetDir, random+ext)

		if isFilenameAvailableAcrossPublicPaths(random, ext) {
			available = true
			break
		}
	}
	if !available {
		http.Error(w, "could not generate a unique filename", http.StatusInternalServerError)
		return
	}

	link := publicFileURL(random + ext)

	// save the file
	outfile, err := os.Create(savePath)
	if err != nil {
		http.Error(w, "error while saving file: "+err.Error(), http.StatusBadRequest)
		return
	}

	_, err = io.Copy(outfile, infile)
	if err != nil {
		http.Error(w, "error while saving file: "+err.Error(), http.StatusBadRequest)
		return
	}
	outfile.Close()

	// return the link as the http body
	w.Write([]byte(link))

	// do this or it doesn't work
	io.Copy(ioutil.Discard, r.Body)
}

func collectGarbage() {
	files, err := ioutil.ReadDir(root)

	if err != nil {
		return
	}

	for _, file := range files {
		fname := file.Name()

		if file.IsDir() || deleteIgnoreRegexp.MatchString(fname) {
			continue
		}

		fileMaxAge := maxAgeForFile(fname)
		if time.Since(file.ModTime()) > fileMaxAge {
			err := os.Remove(filepath.Join(root, fname))

			if err != nil {
				fmt.Println(err)
				continue
			}

			fmt.Printf("Removed %s \n", fname)
		}
	}
}

func generateRandomName(length int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	bytes := make([]byte, length)

	if _, err := cryptorand.Read(bytes); err != nil {
		return "", err
	}

	for i := range bytes {
		bytes[i] = alphabet[int(bytes[i])%len(alphabet)]
	}

	return string(bytes), nil
}

func shouldStorePermanently(r *http.Request) bool {
	v := strings.ToLower(strings.TrimSpace(r.FormValue(permanentFormFlag)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultPermanentUploads
	}
}

func isFilenameAvailableAcrossPublicPaths(name, ext string) bool {
	normalPath := filepath.Join(root, name+ext)
	permanentPath := filepath.Join(root, permanentSubdir, name+ext)
	return !pathExists(normalPath) && !pathExists(permanentPath)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func maxAgeForFile(name string) time.Duration {
	ext := strings.ToLower(filepath.Ext(name))
	if category, ok := filetypes[ext]; ok {
		if retention, ok := categoryMaxAge[category]; ok {
			return retention
		}
	}
	return maxAge
}

func publicFileURL(name string) string {
	return strings.TrimRight(publicBaseURL, "/") + "/" + name
}

func loadFiletypes(path string) error {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	data := make(map[string][]string)
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}

	for category, extensions := range data {
		c := strings.ToLower(strings.TrimSpace(category))
		for _, ext := range extensions {
			e := "." + strings.ToLower(strings.TrimLeft(strings.TrimSpace(ext), "."))
			if e != "." {
				filetypes[e] = c
			}
		}
	}

	return nil
}
