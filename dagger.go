package dagger

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cloudfoundry/dagger/utils"

	"context"

	"github.com/cloudfoundry/libcfbuildpack/packager/cnbpackager"
	"github.com/pkg/errors"
)

const (
	CFLINUXFS3          = "org.cloudfoundry.stacks.cflinuxfs3"
	BIONIC              = "io.buildpacks.stacks.bionic"
	DEFAULT_BUILD_IMAGE = "cfbuildpacks/cflinuxfs3-cnb-experimental:build"
	DEFAULT_RUN_IMAGE   = "cfbuildpacks/cflinuxfs3-cnb-experimental:run"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func PackageBuildpack() (string, error) {
	cmd := exec.Command("./scripts/package.sh")
	cmd.Dir = "../"
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	r := regexp.MustCompile("Buildpack packaged into: (.*)")
	bpDir := r.FindStringSubmatch(string(out))[1]
	return bpDir, nil
}

func PackageLocalCachedBuildpack(bpName, bpRoot string) (string, error) {
	tarFile := utils.CreateUniqueFilepath(bpName)

	defer utils.ReplaceArgs(bpRoot)()
	//defer utils.ReplaceWorkingDirectory(bpRoot)()
	pkgr, err := cnbpackager.DefaultPackager(tarFile)

	if err != nil {
		return "", err
	}
	if err := pkgr.Create(true); err != nil {
		return "", err
	}
	//if err := pkgr.Archive(); err != nil {
	//	return "", err
	//}

	if err := os.RemoveAll(filepath.Join(bpRoot, "dependency-cache")); err != nil {
		return "", err
	}

	return tarFile, err
}

func PackageCachedBuildpack(bpName string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	bpRoot, err := GetLatestBuildpackSource(bpName)
	if err != nil {
		return "", err
	}

	if err := os.Chdir(bpRoot); err != nil {
		return "", err
	}
	defer os.Chdir(wd)

	content, _ := ioutil.ReadDir(bpRoot)
	var extractedDir string

	for _, info := range content {
		if info.IsDir() {
			extractedDir = info.Name()
			break
		}
	}

	return PackageLocalCachedBuildpack(bpName, filepath.Join(bpRoot, extractedDir))
}

func PackageLocalBuildpack(name string) (string, error) {
	cmd := exec.Command("./scripts/package.sh")
	cmd.Dir = fmt.Sprintf("../../%s", name)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	r := regexp.MustCompile("Buildpack packaged into: (.*)")
	bpDir := r.FindStringSubmatch(string(out))[1]
	return bpDir, nil
}

func GetLatestBuildpackSource(name string) (string, error) {
	releaseResp, err := GetCNBReleases(name)
	if err != nil {
		return "", err
	}

	archive, err := utils.DownloadArchive(releaseResp.SourceTarball, name, releaseResp.TagName)
	defer os.Remove(archive)

	if err != nil {
		return "", err
	}
	return utils.ExtractArchive(archive)
}

func GetCNBReleases(cnbName string) (utils.ReleaseResponse, error) {
	result := utils.ReleaseResponse{}

	ctx := context.Background()
	client := utils.GetClient(ctx)
	endpoint := GetApiEndpoint(cnbName)

	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return result, err
	}

	_, err = client.Do(ctx, request, &result)
	if err != nil {
		return result, err
	}

	return result, nil
}

func GetLatestBuildpackRelease(name string) (string, error) {
	releaseResp, err := GetCNBReleases(name)
	if err != nil {
		return "", err
	}

	downloadUrl, tagName, err := GetDownloadUri(releaseResp, 0)
	if err != nil {
		return "", err
	}

	archive, err := utils.DownloadArchive(downloadUrl, name, tagName)
	defer os.Remove(archive)

	if err != nil {
		return "", err
	}
	return utils.ExtractArchive(archive)
}

// This returns the build logs as part of the error case
func PackBuild(appDir string, buildpacks ...string) (*App, error) {
	return PackBuildNamedImage(randomString(16), appDir, buildpacks...)
}

// This pack builds an app from appDir into appImageName, to allow specifying an image name in a test
func PackBuildNamedImage(appImageName, appDir string, buildpacks ...string) (*App, error) {
	buildLogs := &bytes.Buffer{}

	cmd := exec.Command("pack", "build", appImageName, "--builder", "cfbuildpacks/cflinuxfs3-cnb-test-builder")
	for _, bp := range buildpacks {
		cmd.Args = append(cmd.Args, "--buildpack", bp)
	}
	cmd.Dir = appDir
	cmd.Stdout = io.MultiWriter(os.Stdout, buildLogs)
	cmd.Stderr = io.MultiWriter(os.Stderr, buildLogs)
	if err := cmd.Run(); err != nil {
		return nil, errors.Wrap(err, buildLogs.String())
	}

	app := &App{
		buildLogs:   buildLogs,
		Env:         make(map[string]string),
		imageName:   appImageName,
		fixtureName: appDir,
	}
	return app, nil
}

func BuildCFLinuxFS3() error {
	cmd := exec.Command("pack", "stacks", "--no-color")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "could not get stack list %s", out)
	}

	contains, err := regexp.Match(CFLINUXFS3, out)

	if err != nil {
		return errors.Wrap(err, "error running regex match")
	} else if contains {
		fmt.Println("cflinuxfs3 stack already added")
		return nil
	}

	cmd = exec.Command("pack", "add-stack", CFLINUXFS3, "--build-image", DEFAULT_BUILD_IMAGE, "--run-image", DEFAULT_RUN_IMAGE)
	if err = cmd.Run(); err != nil {
		return errors.Wrap(err, "could not add stack")
	}

	return nil
}

type App struct {
	Memory      string
	buildLogs   *bytes.Buffer
	Env         map[string]string
	logProc     *exec.Cmd
	imageName   string
	containerId string
	port        string
	fixtureName string
	healthCheck HealthCheck
}

type HealthCheck struct {
	command  string
	interval string
	timeout  string
}

func (a *App) BuildLogs() string {
	return utils.StripColor(a.buildLogs.String())
}

func (a *App) SetHealthCheck(command, interval, timeout string) {
	a.healthCheck = HealthCheck{
		command:  command,
		interval: interval,
		timeout:  timeout,
	}
}

func (a *App) Start() error {
	buf := &bytes.Buffer{}

	args := []string{"run", "-d", "-P"}
	if a.Memory != "" {
		args = append(args, "--memory", a.Memory)
	}

	if a.healthCheck.command != "" {
		args = append(args, "--health-cmd", a.healthCheck.command)
	}

	if a.healthCheck.interval != "" {
		args = append(args, "--health-interval", a.healthCheck.interval)
	}

	if a.healthCheck.timeout != "" {
		args = append(args, "--health-timeout", a.healthCheck.timeout)
	}

	envTemplate := "%s=%s"
	for k, v := range a.Env {
		envString := fmt.Sprintf(envTemplate, k, v)
		args = append(args, "-e", envString)
	}

	args = append(args, a.imageName)

	cmd := exec.Command("docker", args...)
	cmd.Stdout = buf
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	a.containerId = buf.String()[:12]

	ticker := time.NewTicker(1 * time.Second)
	timeOut := time.After(40 * time.Second)
docker:
	for {
		select {
		case <-ticker.C:
			status, err := exec.Command("docker", "inspect", "-f", "{{.State.Health.Status}}", a.containerId).Output()
			if err != nil {
				return err
			}

			if strings.TrimSpace(string(status)) == "unhealthy" {
				return fmt.Errorf("app failed to start : %s", a.fixtureName)
			}

			if strings.TrimSpace(string(status)) == "healthy" {
				break docker
			}
		case <-timeOut:
			return fmt.Errorf("timed out waiting for app : %s", a.fixtureName)
		}
	}

	cmd = exec.Command("docker", "container", "port", a.containerId)
	cmd.Stdout = buf
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	a.port = strings.TrimSpace(strings.Split(buf.String(), ":")[1])

	return nil
}

func (a *App) Destroy() error {
	if a.containerId == "" {
		return nil
	}

	cmd := exec.Command("docker", "stop", a.containerId)
	if err := cmd.Run(); err != nil {
		return err
	}

	cmd = exec.Command("docker", "rm", a.containerId, "-f", "--volumes")
	if err := cmd.Run(); err != nil {
		return err
	}

	a.containerId = ""
	a.port = ""

	if a.imageName == "" {
		return nil
	}

	cmd = exec.Command("docker", "rmi", a.imageName, "-f")
	if err := cmd.Run(); err != nil {
		return err
	}
	cmd = exec.Command("docker", "image", "prune", "-f")
	if err := cmd.Run(); err != nil {
		return err
	}

	a.imageName = ""
	return nil
}

func (a *App) Files(path string) ([]string, error) {
	cmd := exec.Command("docker", "run", a.imageName, "find", "./..", "-wholename", fmt.Sprintf("*%s*", path))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return []string{}, err
	}
	return strings.Split(string(output), "\n"), nil
}

func (a *App) Info() (cID string, imageID string, cacheID []string, e error) {
	volumes, err := getCacheVolumes()
	if err != nil {
		return "", "", []string{}, err
	}

	return a.containerId, a.imageName, volumes, nil
}

func (a *App) Logs() (string, error) {
	cmd := exec.Command("docker", "logs", a.containerId)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}

	return utils.StripColor(string(output)), nil
}

func (a *App) HTTPGet(path string) (string, map[string][]string, error) {
	resp, err := http.Get("http://localhost:" + a.port + path)
	if err != nil {
		return "", nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("received bad response from application")
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", nil, err
	}

	return string(body), resp.Header, nil
}

func (a *App) HTTPGetBody(path string) (string, error) {
	resp, _, err := a.HTTPGet(path)
	return resp, err
}

func getCacheVolumes() ([]string, error) {
	cmd := exec.Command("docker", "volume", "ls", "-q")
	output, err := cmd.Output()
	if err != nil {
		return []string{}, err
	}

	outputArr := strings.Split(string(output), "\n")
	var finalVolumes []string
	for _, line := range outputArr {
		if strings.Contains(line, "pack-cache") {
			finalVolumes = append(finalVolumes, line)
		}
	}
	return outputArr, nil
}

func randomString(n int) string {
	letterRunes := []rune("abcdefghijklmnopqrstuvwxyz")
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

func GetApiEndpoint(buildpackName string) string {
	return fmt.Sprintf("https://api.github.com/repos/cloudfoundry/%s/releases/latest", buildpackName)
}

func GetDownloadUri(response utils.ReleaseResponse, asset int) (string, string, error) {
	if len(response.Assets) < asset {
		fmt.Printf("release asset %v", response)
		return "", "", fmt.Errorf("there is no corresponding no release asset at index %v", asset)
	}

	return response.Assets[asset].BrowserDownloadURL, response.TagName, nil
}
