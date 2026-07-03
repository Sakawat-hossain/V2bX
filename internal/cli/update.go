package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const releaseAPI = "https://api.github.com/repos/Sakawat-hossain/V2bX/releases/latest"

// Update downloads the latest release binary for this OS/arch, swaps it in
// place of the running executable, and restarts the service if it's running
// under systemd. currentVersion is compared against the latest tag to skip a
// no-op update.
func Update(currentVersion string) error {
	assetName, err := releaseAssetName()
	if err != nil {
		return err
	}

	tag, url, err := latestRelease(assetName)
	if err != nil {
		return err
	}
	if tag == currentVersion {
		fmt.Printf("Already on the latest version (%s).\n", tag)
		return nil
	}
	fmt.Printf("Updating %s -> %s\n", currentVersion, tag)

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	exePath, _ = filepath.EvalSymlinks(exePath)

	bin, err := downloadBinary(url)
	if err != nil {
		return err
	}

	// Write next to the target then rename, so the swap is atomic and a
	// failed download never leaves a half-written binary in place.
	newPath := exePath + ".new"
	if err := os.WriteFile(newPath, bin, 0o755); err != nil {
		return fmt.Errorf("write new binary: %w", err)
	}
	if err := os.Rename(newPath, exePath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("replace binary at %s (try sudo): %w", exePath, err)
	}

	fmt.Printf("Installed %s to %s\n", tag, exePath)
	if ServiceActive() {
		fmt.Println("Restarting service…")
		return RestartService()
	}
	fmt.Println("Update complete. Restart the service with: v2bx restart")
	return nil
}

func releaseAssetName() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("self-update is only supported on linux (this is %s)", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64":
		return "v2bx-linux-amd64.tar.gz", nil
	case "arm64":
		return "v2bx-linux-arm64.tar.gz", nil
	case "arm":
		return "v2bx-linux-armv7.tar.gz", nil
	default:
		return "", fmt.Errorf("no release build for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func latestRelease(assetName string) (tag, assetURL string, err error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(releaseAPI)
	if err != nil {
		return "", "", fmt.Errorf("query latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("release API returned %d", resp.StatusCode)
	}

	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", fmt.Errorf("decode release: %w", err)
	}
	for _, a := range rel.Assets {
		if a.Name == assetName {
			return rel.TagName, a.URL, nil
		}
	}
	return "", "", fmt.Errorf("release %s has no asset %q", rel.TagName, assetName)
}

// downloadBinary fetches the release tarball and returns the contained "v2bx"
// binary bytes.
func downloadBinary(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tarball: %w", err)
		}
		if filepath.Base(hdr.Name) == "v2bx" && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("tarball did not contain a v2bx binary")
}
