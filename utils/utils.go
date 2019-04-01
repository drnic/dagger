package utils

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/cloudfoundry/libcfbuildpack/helper"

	"github.com/google/go-github/v24/github"

	"golang.org/x/oauth2"
)

type ReleaseResponse struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
	SourceTarball string `json:"tarball_url"`
}

var downloadCache sync.Map

func init() {
	downloadCache = sync.Map{}
	rand.Seed(time.Now().UnixNano())
}

func GetClient(ctx context.Context) *github.Client {
	git_token := os.Getenv("GIT_TOKEN")

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: git_token},
	)
	tc := oauth2.NewClient(ctx, ts)

	client := github.NewClient(http.DefaultClient)
	if git_token != "" {
		fmt.Println("Using unauthorized github api, consider setting the GIT_TOKEN environment variable")
		fmt.Println("More info on Github tokens here: https://help.github.com/en/articles/creating-a-personal-access-token-for-the-command-line")
		client = github.NewClient(tc)
	}

	return client
}

func RandStringRunes(n int) string {
	letterRunes := []rune("abcdefghijklmnopqrstuvwxyz")
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

func CreateUniqueFilepath(name string) string {
	return filepath.Join(os.TempDir(), name+"-"+RandStringRunes(16))
}

func StripColor(input string) string {
	const ansi = "[\u001B\u009B][[\\]()#;?]*(?:(?:(?:[a-zA-Z\\d]*(?:;[a-zA-Z\\d]*)*)?\u0007)|(?:(?:\\d{1,4}(?:;\\d{0,4})*)?[\\dA-PRZcf-ntqry=><~]))"

	var re = regexp.MustCompile(ansi)
	return re.ReplaceAllString(input, "")
}

func ExtractArchive(tarFile string) (string, error) {
	dest, err := ioutil.TempDir("", "")
	fmt.Println("Extracting tar file to ", dest)
	if err != nil {
		return "", err
	}
	defer os.Remove(tarFile)

	return dest, helper.ExtractTarGz(tarFile, dest, 0)
}

// Doesn't account for same buildpack having source and release downloads
func DownloadArchive(uri string, name string, tagName string) (string, error) {
	contents, found := downloadCache.Load(name + tagName)
	if !found {
		buildpackResp, err := http.Get(uri)
		if err != nil {
			return "", err
		}
		defer buildpackResp.Body.Close()

		contents, err = ioutil.ReadAll(buildpackResp.Body)
		if err != nil {
			return "", err
		}

		uriSha := sha256.New()
		uriSha.Write([]byte(uri))
		sha := base64.URLEncoding.EncodeToString(uriSha.Sum(nil))

		downloadCache.Store(name+tagName+sha, contents)
	}

	downloadFile, err := ioutil.TempFile("", "")
	if err != nil {
		return "", err
	}

	_, err = io.Copy(downloadFile, bytes.NewReader(contents.([]byte)))
	if err != nil {
		return "", err
	}
	return downloadFile.Name(), nil
}

func ReplaceArgs(args ...string) func() {
	previous := os.Args
	os.Args = args

	return func() { os.Args = previous }
}

func ReplaceWorkingDirectory(dir string) func() {
	previous, err := os.Getwd()
	if err != nil {
		fmt.Println("error getting current working directory:", err.Error())
		os.Exit(1)
	}

	if err = os.Chdir(dir); err != nil {
		fmt.Println("error changing the current working directory:", err.Error())
		os.Exit(1)
	}

	return func() {
		if err := os.Chdir(previous); err != nil {
			fmt.Println("error reverting the working directory:", err.Error())
			os.Exit(1)
		}
	}
}
